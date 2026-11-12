package app

// Entity and input types. JSON field names are part of the CLI contract.

// Customer is a repair-shop customer record.
type Customer struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Phone      string `json:"phone"`
	Email      string `json:"email"`
	Address    string `json:"address"`
	Notes      string `json:"notes"`
	ArchivedAt string `json:"archived_at,omitempty"`
	Revision   string `json:"revision"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// Repair is a repair belonging to one customer, or an unassigned intake.
type Repair struct {
	ID            int64  `json:"id"`
	CustomerID    int64  `json:"customer_id"`
	Unassigned    bool   `json:"unassigned,omitempty"`
	CustomerName  string `json:"customer_name,omitempty"`
	Device        string `json:"device"` // compatibility display: brand + model, else device_type
	DeviceType    string `json:"device_type"`
	Brand         string `json:"brand"`
	Model         string `json:"model"`
	Serial        string `json:"serial"`
	Issue         string `json:"issue"`
	ReportedIssue string `json:"reported_issue"`
	Stage         string `json:"stage"`
	Notes         string `json:"notes"`
	PickupAt      string `json:"pickup_at,omitempty"`
	PickedUpBy    string `json:"picked_up_by,omitempty"`
	PickupNotes   string `json:"pickup_notes,omitempty"`
	ArchivedAt    string `json:"archived_at,omitempty"`
	Revision      string `json:"revision"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// Entry is one work-log entry on a repair. Soft-deleted entries keep their
// rows and revisions (DeletedAt set); they disappear from normal views.
type Entry struct {
	ID        int64  `json:"id"`
	RepairID  int64  `json:"repair_id"`
	Kind      string `json:"kind"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	DeletedAt string `json:"deleted_at,omitempty"`
}

// EntryRevision is the immutable audit snapshot of an entry before an edit.
type EntryRevision struct {
	ID            int64  `json:"id"`
	EntryID       int64  `json:"entry_id"`
	Actor         string `json:"actor"`
	PrevKind      string `json:"prev_kind"`
	PrevBody      string `json:"prev_body"`
	PrevCreatedAt string `json:"prev_created_at"`
	CreatedAt     string `json:"created_at"`
}

// Activity is one audit record on a repair.
type Activity struct {
	ID        int64  `json:"id"`
	RepairID  int64  `json:"repair_id"`
	Actor     string `json:"actor"`
	Action    string `json:"action"`
	Detail    string `json:"detail"`
	CreatedAt string `json:"created_at"`
}

// CustomerHistory is one audit record on a customer.
type CustomerHistory struct {
	ID         int64  `json:"id"`
	CustomerID int64  `json:"customer_id"`
	Actor      string `json:"actor"`
	Action     string `json:"action"`
	Detail     string `json:"detail"`
	CreatedAt  string `json:"created_at"`
}

// RepairDetail aggregates a repair with its log and audit trail.
type RepairDetail struct {
	Repair
	Entries  []Entry    `json:"entries"`
	Activity []Activity `json:"activity"`
}

// Page is one page of a stable, deterministically ordered listing.
type Page[T any] struct {
	Items   []T `json:"items"`
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
	Total   int `json:"total"`
}

// CustomerInput is the create payload for a customer.
type CustomerInput struct {
	Name    string
	Phone   string
	Email   string
	Address string
	Notes   string
}

// CustomerPatch carries optional customer updates; nil fields are unchanged.
type CustomerPatch struct {
	Name    *string
	Phone   *string
	Email   *string
	Address *string
	Notes   *string
}

// RepairInput is the create payload for a repair.
type RepairInput struct {
	CustomerID int64
	Device     string // compatibility alias for DeviceType
	DeviceType string
	Brand      string
	Model      string
	Serial     string
	Issue      string
	Notes      string
}

// RepairPatch carries optional repair-detail updates; nil fields are unchanged.
// CustomerID is a pointer so nil means unchanged and 0 means clear the customer.
type RepairPatch struct {
	CustomerID *int64
	Device     *string // compatibility alias for DeviceType
	DeviceType *string
	Brand      *string
	Model      *string
	Serial     *string
	Issue      *string
	Notes      *string
}

// CustomerFilter selects and pages customers.
type CustomerFilter struct {
	Search          string
	Page            int
	PerPage         int
	IncludeArchived bool
}

// RepairFilter selects and pages repairs.
type RepairFilter struct {
	Search          string
	Stage           string
	CustomerID      int64
	PickedUp        *bool // nil = any; true = collected; false = still in shop
	Page            int
	PerPage         int
	IncludeArchived bool
}
