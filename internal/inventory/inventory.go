package inventory

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"sbcbackend/internal/logger"
)

type Service struct {
	// Rich data structures (from unified format)
	memberships map[string]MembershipItem
	products    map[string]ProductItem
	fees        map[string]FeeItem
	events      map[string]EventConfig

	// Quick lookup maps (for performance and backward compatibility)
	membershipPrices map[string]float64
	productPrices    map[string]float64
	feePrices        map[string]float64

	// Cache management
	lastLoaded time.Time
	mutex      sync.RWMutex
}

func NewService() *Service {
	return &Service{
		memberships:      make(map[string]MembershipItem),
		products:         make(map[string]ProductItem),
		fees:             make(map[string]FeeItem),
		events:           make(map[string]EventConfig),
		membershipPrices: make(map[string]float64),
		productPrices:    make(map[string]float64),
		feePrices:        make(map[string]float64),
	}
}

// Smart loader - detects format based on number of paths
func (s *Service) LoadInventory(paths ...string) error {
	switch len(paths) {
	case 1:
		// Single file = unified inventory.json
		return s.LoadFromUnifiedFile(paths[0])
	case 4:
		// Four files = legacy format: memberships, products, fees, events
		return s.LoadFromCurrentFiles(paths[0], paths[1], paths[2], paths[3])
	default:
		return fmt.Errorf("invalid number of paths: expected 1 (unified) or 4 (legacy), got %d", len(paths))
	}
}

// Load from unified inventory.json file
func (s *Service) LoadFromUnifiedFile(inventoryPath string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	logger.LogInfo("Loading inventory from unified file: %s", inventoryPath)

	data, err := os.ReadFile(inventoryPath)
	if err != nil {
		return fmt.Errorf("failed to read inventory file: %w", err)
	}

	var inventory InventoryData
	if err := json.Unmarshal(data, &inventory); err != nil {
		return fmt.Errorf("failed to parse inventory file: %w", err)
	}

	// Populate internal maps from unified structure
	s.populateFromUnified(inventory)
	s.lastLoaded = time.Now()

	logger.LogInfo("Successfully loaded unified inventory: %d memberships, %d products, %d fees, %d events",
		len(s.memberships), len(s.products), len(s.fees), len(s.events))

	return nil
}

// Load from current 3-file + event structure (backward compatibility)
func (s *Service) LoadFromCurrentFiles(membershipsPath, productsPath, feesPath, eventsPath string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	logger.LogInfo("Loading inventory from legacy files")

	// Load membership files
	memberships, err := s.loadLegacyMemberships(membershipsPath)
	if err != nil {
		return fmt.Errorf("failed to load memberships: %w", err)
	}

	products, err := s.loadLegacyProducts(productsPath)
	if err != nil {
		return fmt.Errorf("failed to load products: %w", err)
	}

	fees, err := s.loadLegacyFees(feesPath)
	if err != nil {
		return fmt.Errorf("failed to load fees: %w", err)
	}

	events, err := s.loadLegacyEvents(eventsPath)
	if err != nil {
		return fmt.Errorf("failed to load events: %w", err)
	}

	// Populate internal maps from legacy data
	s.populateFromLegacy(memberships, products, fees, events)
	s.lastLoaded = time.Now()

	logger.LogInfo("Successfully loaded legacy inventory: %d memberships, %d products, %d fees, %d events",
		len(s.memberships), len(s.products), len(s.fees), len(s.events))

	return nil
}

// Check if cache needs refresh (optional future enhancement)
func (s *Service) IsStale(maxAge time.Duration) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return time.Since(s.lastLoaded) > maxAge
}

// Get cache age for debugging
func (s *Service) CacheAge() time.Duration {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return time.Since(s.lastLoaded)
}

// Populate from unified inventory structure
func (s *Service) populateFromUnified(inventory InventoryData) {
	// Clear existing data
	s.memberships = make(map[string]MembershipItem)
	s.products = make(map[string]ProductItem)
	s.fees = make(map[string]FeeItem)
	s.events = make(map[string]EventConfig)
	s.membershipPrices = make(map[string]float64)
	s.productPrices = make(map[string]float64)
	s.feePrices = make(map[string]float64)

	// Populate memberships
	for _, item := range inventory.Memberships {
		if item.Available {
			s.memberships[item.Name] = item
			s.membershipPrices[item.Name] = item.Price
		}
	}

	// Populate products
	for _, item := range inventory.Products {
		if item.Available {
			s.products[item.Name] = item
			s.productPrices[item.Name] = item.Price
		}
	}

	// Populate fees
	for _, item := range inventory.Fees {
		if item.Available {
			s.fees[item.Name] = item
			s.feePrices[item.Name] = item.Price
		}
	}

	// Populate events
	s.events = inventory.Events
}

// Populate from legacy file data
func (s *Service) populateFromLegacy(memberships, products, fees []LegacyItem, events map[string]EventConfig) {
	// Clear existing data
	s.memberships = make(map[string]MembershipItem)
	s.products = make(map[string]ProductItem)
	s.fees = make(map[string]FeeItem)
	s.events = make(map[string]EventConfig)
	s.membershipPrices = make(map[string]float64)
	s.productPrices = make(map[string]float64)
	s.feePrices = make(map[string]float64)

	// Convert legacy memberships
	for _, item := range memberships {
		membershipItem := MembershipItem{
			ID:        item.Name, // Use name as ID for legacy
			Name:      item.Name,
			Price:     item.Price,
			Available: true,
		}
		s.memberships[item.Name] = membershipItem
		s.membershipPrices[item.Name] = item.Price
	}

	// Convert legacy products
	for _, item := range products {
		productItem := ProductItem{
			ID:        item.Name,
			Name:      item.Name,
			Price:     item.Price,
			Available: true,
		}
		s.products[item.Name] = productItem
		s.productPrices[item.Name] = item.Price
	}

	// Convert legacy fees
	for _, item := range fees {
		feeItem := FeeItem{
			ID:        item.Name,
			Name:      item.Name,
			Price:     item.Price,
			Available: true,
		}
		s.fees[item.Name] = feeItem
		s.feePrices[item.Name] = item.Price
	}

	// Events are already in the right format
	s.events = events
}

// Legacy file loading methods
func (s *Service) loadLegacyMemberships(path string) ([]LegacyItem, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read memberships file: %w", err)
	}

	var items []LegacyItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("failed to parse memberships file: %w", err)
	}

	return items, nil
}

func (s *Service) loadLegacyProducts(path string) ([]LegacyItem, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read products file: %w", err)
	}

	var items []LegacyItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("failed to parse products file: %w", err)
	}

	return items, nil
}

func (s *Service) loadLegacyFees(path string) ([]LegacyItem, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read fees file: %w", err)
	}

	var items []LegacyItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("failed to parse fees file: %w", err)
	}

	return items, nil
}

func (s *Service) loadLegacyEvents(path string) (map[string]EventConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read events file: %w", err)
	}

	var events map[string]EventConfig
	if err := json.Unmarshal(data, &events); err != nil {
		return nil, fmt.Errorf("failed to parse events file: %w", err)
	}

	return events, nil
}

// =============================================================================
// MEMBERSHIP VALIDATION AND CALCULATION METHODS
// =============================================================================

// ValidateMembership checks if a membership type exists and is available
func (s *Service) ValidateMembership(name string) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	membership, exists := s.memberships[name]
	return exists && membership.Available
}

// ValidateProduct checks if a product exists and is available
func (s *Service) ValidateProduct(name string) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	product, exists := s.products[name]
	return exists && product.Available
}

// ValidateFee checks if a fee exists and is available
func (s *Service) ValidateFee(name string) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	fee, exists := s.fees[name]
	return exists && fee.Available
}

// ValidateAllSelections validates an entire membership selection
func (s *Service) ValidateAllSelections(membership string, addons []string, fees map[string]int) error {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	// Validate membership
	if !s.ValidateMembership(membership) {
		return fmt.Errorf("invalid membership: %s", membership)
	}

	// Validate addons/products
	for _, addon := range addons {
		if !s.ValidateProduct(addon) {
			return fmt.Errorf("invalid addon: %s", addon)
		}
	}

	// Validate fees
	for feeName := range fees {
		if !s.ValidateFee(feeName) {
			return fmt.Errorf("invalid fee: %s", feeName)
		}
	}

	return nil
}

// CalculateMembershipTotal calculates the total cost with tamper protection
func (s *Service) CalculateMembershipTotal(membership string, addons []string, fees map[string]int, donation float64, coverFees bool) (float64, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	// Validate all selections first
	if err := s.ValidateAllSelections(membership, addons, fees); err != nil {
		return 0, fmt.Errorf("validation failed: %w", err)
	}

	// Calculate base total
	total := s.membershipPrices[membership]

	// Add addon prices
	for _, addon := range addons {
		total += s.productPrices[addon]
	}

	// Add fee prices (quantity * price)
	for feeName, quantity := range fees {
		if quantity > 0 {
			total += s.feePrices[feeName] * float64(quantity)
		}
	}

	// Add donation
	if donation > 0 {
		total += donation
	}

	// Apply processing fees if requested
	if coverFees {
		total = total*1.02 + 0.49
	}

	// Round to 2 decimal places to prevent floating point issues
	total = float64(int(total*100+0.5)) / 100

	return total, nil
}

// GetMembershipPrice returns the price for a specific membership
func (s *Service) GetMembershipPrice(name string) (float64, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	price, exists := s.membershipPrices[name]
	return price, exists
}

// GetProductPrice returns the price for a specific product
func (s *Service) GetProductPrice(name string) (float64, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	price, exists := s.productPrices[name]
	return price, exists
}

// GetFeePrice returns the price for a specific fee
func (s *Service) GetFeePrice(name string) (float64, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	price, exists := s.feePrices[name]
	return price, exists
}

// =============================================================================
// EVENT METHODS (for future integration)
// =============================================================================

// GetEventConfig returns the configuration for a specific event
func (s *Service) GetEventConfig(eventName string) (EventConfig, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	config, exists := s.events[eventName]
	return config, exists
}

// ValidateEventSelection validates event selections
func (s *Service) ValidateEventSelection(eventName string, studentSelections map[string]map[string]bool, sharedSelections map[string]int) error {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	eventConfig, exists := s.events[eventName]
	if !exists {
		return fmt.Errorf("event not found: %s", eventName)
	}

	// Validate student selections
	for studentIndex, selections := range studentSelections {
		for optionKey := range selections {
			if _, exists := eventConfig.PerStudentOptions[optionKey]; !exists {
				return fmt.Errorf("invalid per-student option for student %s: %s", studentIndex, optionKey)
			}
		}
	}

	// Validate shared selections
	for optionKey, quantity := range sharedSelections {
		option, exists := eventConfig.SharedOptions[optionKey]
		if !exists {
			return fmt.Errorf("invalid shared option: %s", optionKey)
		}

		// Check max quantity if specified
		if option.MaxQuantity > 0 && quantity > option.MaxQuantity {
			return fmt.Errorf("quantity %d exceeds maximum %d for option %s", quantity, option.MaxQuantity, optionKey)
		}
	}

	return nil
}

// CalculateEventTotal calculates total cost for event selections
func (s *Service) CalculateEventTotal(eventName string, studentSelections map[string]map[string]bool, sharedSelections map[string]int, coverFees bool) (float64, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	// Validate selections first
	if err := s.ValidateEventSelection(eventName, studentSelections, sharedSelections); err != nil {
		return 0, fmt.Errorf("validation failed: %w", err)
	}

	eventConfig := s.events[eventName]
	total := 0.0

	// Calculate per-student options
	for _, selections := range studentSelections {
		for optionKey, isSelected := range selections {
			if isSelected {
				option := eventConfig.PerStudentOptions[optionKey]
				total += option.Price
			}
		}
	}

	// Calculate shared options
	for optionKey, quantity := range sharedSelections {
		if quantity > 0 {
			option := eventConfig.SharedOptions[optionKey]
			total += option.Price * float64(quantity)
		}
	}

	// Apply processing fees if requested
	if coverFees {
		total = total*1.02 + 0.49
	}

	// Round to 2 decimal places
	total = float64(int(total*100+0.5)) / 100

	return total, nil
}

// =============================================================================
// INFORMATIONAL METHODS
// =============================================================================

// GetAvailableMemberships returns all available memberships
func (s *Service) GetAvailableMemberships() []MembershipItem {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var memberships []MembershipItem
	for _, membership := range s.memberships {
		if membership.Available {
			memberships = append(memberships, membership)
		}
	}

	return memberships
}

// GetAvailableProducts returns all available products
func (s *Service) GetAvailableProducts() []ProductItem {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var products []ProductItem
	for _, product := range s.products {
		if product.Available {
			products = append(products, product)
		}
	}

	return products
}

// GetAvailableFees returns all available fees
func (s *Service) GetAvailableFees() []FeeItem {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var fees []FeeItem
	for _, fee := range s.fees {
		if fee.Available {
			fees = append(fees, fee)
		}
	}

	return fees
}

// GetStats returns inventory statistics for debugging/monitoring
func (s *Service) GetStats() map[string]interface{} {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return map[string]interface{}{
		"memberships_count": len(s.memberships),
		"products_count":    len(s.products),
		"fees_count":        len(s.fees),
		"events_count":      len(s.events),
		"last_loaded":       s.lastLoaded,
		"cache_age":         time.Since(s.lastLoaded).String(),
	}
}
