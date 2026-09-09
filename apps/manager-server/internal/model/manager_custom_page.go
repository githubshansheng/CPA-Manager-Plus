package model

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	ManagerCustomPageMaxCount       = 50
	ManagerCustomPageMaxIDLength    = 64
	ManagerCustomPageMaxTitleLength = 80
	ManagerCustomPageMaxURLLength   = 2048
)

var managerCustomPageIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// NormalizeManagerCustomPages produces the canonical representation stored in
// settings. A non-nil empty slice intentionally means "remove all custom
// pages", while nil lets older clients omit the field without clearing it.
func NormalizeManagerCustomPages(input []ManagerCustomPageConfig) []ManagerCustomPageConfig {
	if input == nil {
		return nil
	}

	normalized := make([]ManagerCustomPageConfig, len(input))
	for index, page := range input {
		normalized[index] = ManagerCustomPageConfig{
			ID:    strings.TrimSpace(page.ID),
			Title: strings.TrimSpace(page.Title),
			URL:   strings.TrimSpace(page.URL),
		}
	}
	return normalized
}

func ValidateManagerCustomPages(input []ManagerCustomPageConfig) error {
	if len(input) > ManagerCustomPageMaxCount {
		return fmt.Errorf(
			"invalid custom page configuration: at most %d custom pages are allowed",
			ManagerCustomPageMaxCount,
		)
	}

	seenIDs := make(map[string]struct{}, len(input))
	for index, page := range NormalizeManagerCustomPages(input) {
		position := index + 1
		if page.ID == "" || len(page.ID) > ManagerCustomPageMaxIDLength || !managerCustomPageIDPattern.MatchString(page.ID) {
			return fmt.Errorf("invalid custom page configuration: item %d has an invalid id", position)
		}
		canonicalID := strings.ToLower(page.ID)
		if _, exists := seenIDs[canonicalID]; exists {
			return fmt.Errorf("invalid custom page configuration: item %d has a duplicate id", position)
		}
		seenIDs[canonicalID] = struct{}{}

		if page.Title == "" {
			return fmt.Errorf("invalid custom page configuration: item %d title is required", position)
		}
		if utf8.RuneCountInString(page.Title) > ManagerCustomPageMaxTitleLength {
			return fmt.Errorf(
				"invalid custom page configuration: item %d title exceeds %d characters",
				position,
				ManagerCustomPageMaxTitleLength,
			)
		}

		if page.URL == "" {
			return fmt.Errorf("invalid custom page configuration: item %d URL is required", position)
		}
		if len(page.URL) > ManagerCustomPageMaxURLLength {
			return fmt.Errorf(
				"invalid custom page configuration: item %d URL exceeds %d characters",
				position,
				ManagerCustomPageMaxURLLength,
			)
		}
		parsed, err := url.Parse(page.URL)
		if err != nil || parsed.Hostname() == "" {
			return fmt.Errorf("invalid custom page configuration: item %d URL is invalid", position)
		}
		scheme := strings.ToLower(parsed.Scheme)
		if scheme != "http" && scheme != "https" {
			return fmt.Errorf(
				"invalid custom page configuration: item %d URL must use http or https",
				position,
			)
		}
		if parsed.User != nil {
			return fmt.Errorf(
				"invalid custom page configuration: item %d URL must not contain credentials",
				position,
			)
		}
	}
	return nil
}
