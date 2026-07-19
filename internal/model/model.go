package model

import "time"

type Actor struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Kind        string    `json:"kind"`
	Role        string    `json:"role"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (a Actor) IsAdmin() bool   { return a.Active && a.Kind == "human" && a.Role == "admin" }
func (a Actor) IsService() bool { return a.Active && a.Kind == "service" }

type APIToken struct {
	ID         string     `json:"id"`
	ActorID    string     `json:"actor_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Project struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	CreatedBy   string     `json:"created_by"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type Task struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	ProjectID   string    `json:"project_id"`
	CreatedBy   string    `json:"created_by"`
	AssignedTo  string    `json:"assigned_to"`
	Status      string    `json:"status"`
	Result      string    `json:"result"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	BlockedBy   []TaskRef `json:"blocked_by"`
	IsBlocked   bool      `json:"is_blocked"`
	Actionable  bool      `json:"actionable"`
}

func (t Task) Terminal() bool {
	return t.Status == "done" || t.Status == "failed" || t.Status == "cancelled"
}

type TaskRef struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type TaskEvent struct {
	ID        string         `json:"id"`
	TaskID    string         `json:"task_id"`
	ActorID   string         `json:"actor_id"`
	EventType string         `json:"event_type"`
	Changes   map[string]any `json:"changes"`
	CreatedAt time.Time      `json:"created_at"`
}

type TaskFilter struct {
	Statuses     []string
	AssignedTo   string
	CreatedBy    string
	ProjectID    string
	UpdatedAfter *time.Time
	Actionable   *bool
	Query        string
	Cursor       string
	Limit        int
}

type TaskPage struct {
	Data       []Task `json:"data"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type DependencyResult struct {
	TaskID string `json:"task_id"`
	Title  string `json:"title"`
	Result string `json:"result"`
}

type WorkDelivery struct {
	Delivery          string             `json:"delivery"`
	Task              Task               `json:"task"`
	DependencyResults []DependencyResult `json:"dependency_results"`
}
