// Package api holds the JSON types of a made-up order service.
package api

import (
	"time"

	"example.com/orders/money"
)

// Meta is shared by every resource.
type Meta struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

// Order is returned by GET /orders/{id}.
type Order struct {
	Meta
	CustomerID string      `json:"customer_id"`
	Status     string      `json:"status"`
	Items      []Item      `json:"items"`
	Total      money.Cents `json:"total"`
	Quantity   int         `json:"quantity"`
	Note       string      `json:"note"`
	Shipping   struct {
		Method string `json:"method"`
		Days   int    `json:"days"`
	} `json:"shipping"`
	Tags     map[string]string `json:"tags"`
	internal string
}

// Item is one line of an order.
type Item struct {
	SKU   string  `json:"sku"`
	Count int     `json:"count"`
	Price float64 `json:"price"`
}

// Customer is returned by GET /customers/{id}.
type Customer struct {
	Meta
	Email string `json:"email"`
	Name  string `json:"name"`
}
