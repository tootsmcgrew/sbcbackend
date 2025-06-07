package inventory

// Unified inventory structure for inventory.json
type InventoryData struct {
	Memberships []MembershipItem       `json:"memberships"`
	Products    []ProductItem          `json:"products"`
	Fees        []FeeItem              `json:"fees"`
	Events      map[string]EventConfig `json:"events"`
}

// Individual item types
type MembershipItem struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Description string  `json:"description,omitempty"`
	Available   bool    `json:"available"`
}

type ProductItem struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Category  string  `json:"category,omitempty"`
	Available bool    `json:"available"`
}

type FeeItem struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Event     string  `json:"event,omitempty"`
	Available bool    `json:"available"`
}

// Event structures (compatible with existing event-purchases.json)
type EventOption struct {
	Label          string  `json:"label"`
	Price          float64 `json:"price"`
	Required       bool    `json:"required,omitempty"`
	IsFood         bool    `json:"is_food,omitempty"`
	MaxQuantity    int     `json:"max_quantity,omitempty"`
	ExclusiveGroup string  `json:"exclusive_group,omitempty"`
}

type EventConfig struct {
	PerStudentOptions map[string]EventOption `json:"per_student_options"`
	SharedOptions     map[string]EventOption `json:"shared_options"`
}

// Legacy format structures (for loading existing files)
type LegacyItem struct {
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}
