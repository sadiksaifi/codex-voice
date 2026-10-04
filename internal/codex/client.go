package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

type notification struct {
	Method string
	Params json.RawMessage
}

type response struct {
	Result json.RawMessage
	Err    error
}

// Client speaks JSON-RPC over pipes supplied by the caller.
type Client struct {
	ctx     context.Context
	writer  io.Writer
	writeMu sync.Mutex
	mu      sync.Mutex
	next    int
	pending map[int]chan response
	events  chan notification
	done    chan struct{}
	err     error
}

func NewClient(
	ctx context.Context,
	reader io.Reader,
	writer io.Writer,
) *Client {
	c := &Client{
		ctx:    ctx,
		writer: writer, pending: make(map[int]chan response),
		events: make(chan notification, 128), done: make(chan struct{}),
	}
	go c.read(reader)
	return c
}

func (c *Client) write(message any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return json.NewEncoder(c.writer).Encode(message)
}

func (c *Client) Call(
	ctx context.Context,
	method string,
	params, result any,
) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.next++
	id := c.next
	reply := make(chan response, 1)
	c.pending[id] = reply
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.write(
		map[string]any{"id": id, "method": method, "params": params},
	); err != nil {
		return err
	}
	select {
	case message := <-reply:
		if message.Err != nil {
			return message.Err
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(message.Result, result)
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		return err
	}
}

func (c *Client) notify(
	method string,
) error {
	return c.write(map[string]any{"method": method})
}

func (c *Client) read(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 8*1024*1024)
	var readErr error
	defer func() {
		if readErr == nil {
			readErr = io.EOF
		}
		c.mu.Lock()
		c.err = fmt.Errorf("codex connection closed: %w", readErr)
		c.mu.Unlock()
		close(c.done)
		close(c.events)
	}()
	for scanner.Scan() {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			readErr = err
			return
		}
		if message.Method != "" && len(message.ID) > 0 {
			// The demo never grants approval to write files or expand permissions.
			var result any
			switch message.Method {
			case "item/commandExecution/requestApproval",
				"item/fileChange/requestApproval":
				result = map[string]string{"decision": "decline"}
			case "item/permissions/requestApproval":
				result = map[string]any{
					"permissions": map[string]any{},
					"scope":       "turn",
				}
			case "mcpServer/elicitation/request":
				result = map[string]any{"action": "decline", "content": nil}
			default:
				if err := c.write(
					map[string]any{
						"id": message.ID,
						"error": map[string]any{
							"code":    -32601,
							"message": "This voice client does not support this request",
						},
					},
				); err != nil {
					readErr = err
					return
				}
				continue
			}
			if err := c.write(
				map[string]any{"id": message.ID, "result": result},
			); err != nil {
				readErr = err
				return
			}
		} else if message.Method != "" {
			// Only forward notifications the voice adapter consumes.
			if message.Method != "thread/realtime/sdp" &&
				message.Method != "thread/realtime/error" &&
				message.Method != "item/agentMessage/delta" &&
				message.Method != "thread/realtime/closed" {
				continue
			}
			select {
			case c.events <- notification{Method: message.Method, Params: message.Params}:
			case <-c.ctx.Done():
				readErr = c.ctx.Err()
				return
			}
		} else if len(message.ID) > 0 {
			var id int
			if json.Unmarshal(message.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			reply := c.pending[id]
			c.mu.Unlock()
			if reply != nil {
				item := response{Result: message.Result}
				if message.Error != nil {
					item.Err = fmt.Errorf(
						"%s (RPC %d)",
						message.Error.Message,
						message.Error.Code,
					)
				}
				reply <- item
			}
		}
	}
	readErr = scanner.Err()
}
