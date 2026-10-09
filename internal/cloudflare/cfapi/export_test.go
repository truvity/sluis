package cfapi

import "time"

// WithBackoff shortens the wait between retries.
func WithBackoff(d time.Duration) Option {
	return func(c *Client) { c.backoff = d }
}
