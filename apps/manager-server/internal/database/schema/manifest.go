package schema

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type TableClass string

const (
	ClassAuthoritative TableClass = "authoritative"
	ClassDerived       TableClass = "derived"
	ClassInternal      TableClass = "internal"
)

type ColumnKind string

const (
	KindInteger ColumnKind = "integer"
	KindReal    ColumnKind = "real"
	KindText    ColumnKind = "text"
	KindBlob    ColumnKind = "blob"
)

type Column struct {
	Name               string     `json:"name"`
	Kind               ColumnKind `json:"kind"`
	Nullable           bool       `json:"nullable"`
	Default            *string    `json:"default,omitempty"`
	PrimaryKeyPosition int        `json:"primaryKeyPosition,omitempty"`
	AutoIncrement      bool       `json:"autoIncrement,omitempty"`
	MySQLType          string     `json:"mysqlType"`
}

type ForeignKey struct {
	Name      string `json:"name"`
	Column    string `json:"column"`
	RefTable  string `json:"refTable"`
	RefColumn string `json:"refColumn"`
	OnUpdate  string `json:"onUpdate,omitempty"`
	OnDelete  string `json:"onDelete,omitempty"`
}

type Index struct {
	Name      string   `json:"name"`
	Columns   []string `json:"columns,omitempty"`
	Unique    bool     `json:"unique,omitempty"`
	SourceDDL string   `json:"sourceDdl,omitempty"`
}

type Table struct {
	Name        string       `json:"name"`
	Class       TableClass   `json:"class"`
	MySQLOnly   bool         `json:"mysqlOnly,omitempty"`
	SQLiteOnly  bool         `json:"sqliteOnly,omitempty"`
	Columns     []Column     `json:"columns"`
	ForeignKeys []ForeignKey `json:"foreignKeys,omitempty"`
	Indexes     []Index      `json:"indexes,omitempty"`
	SourceDDL   string       `json:"sourceDdl,omitempty"`
}

type Manifest struct {
	Version int     `json:"version"`
	Tables  []Table `json:"tables"`
}

var (
	currentOnce     sync.Once
	currentManifest Manifest
	currentErr      error
)

func Current() Manifest {
	currentOnce.Do(func() {
		if err := json.Unmarshal([]byte(manifestJSON), &currentManifest); err != nil {
			currentErr = fmt.Errorf("decode database schema manifest: %w", err)
			return
		}
		if err := currentManifest.Validate(); err != nil {
			currentErr = err
		}
	})
	if currentErr != nil {
		panic(currentErr)
	}
	return cloneManifest(currentManifest)
}

func (m Manifest) Validate() error {
	if m.Version < 1 {
		return errorsf("schema manifest version must be positive")
	}
	tables := map[string]struct{}{}
	tablesByName := map[string]Table{}
	tableColumns := map[string]map[string]struct{}{}
	for _, table := range m.Tables {
		if !validIdentifier(table.Name) {
			return errorsf("invalid table name %q", table.Name)
		}
		if _, exists := tables[table.Name]; exists {
			return errorsf("duplicate table %q", table.Name)
		}
		tables[table.Name] = struct{}{}
		tablesByName[table.Name] = table
		columns := map[string]struct{}{}
		tableColumns[table.Name] = columns
		pkPositions := map[int]struct{}{}
		autoIncrementColumns := 0
		for _, column := range table.Columns {
			if !validIdentifier(column.Name) {
				return errorsf("invalid column %s.%q", table.Name, column.Name)
			}
			if _, exists := columns[column.Name]; exists {
				return errorsf("duplicate column %s.%s", table.Name, column.Name)
			}
			columns[column.Name] = struct{}{}
			if column.MySQLType == "" {
				return errorsf("column %s.%s has no MySQL type", table.Name, column.Name)
			}
			if column.PrimaryKeyPosition > 0 {
				if _, exists := pkPositions[column.PrimaryKeyPosition]; exists {
					return errorsf("duplicate primary key position in %s", table.Name)
				}
				pkPositions[column.PrimaryKeyPosition] = struct{}{}
			}
			if column.AutoIncrement {
				autoIncrementColumns++
				if column.Kind != KindInteger || column.PrimaryKeyPosition != 1 {
					return errorsf("AUTO_INCREMENT column %s.%s must be the first integer primary-key column", table.Name, column.Name)
				}
			}
		}
		for position := 1; position <= len(pkPositions); position++ {
			if _, exists := pkPositions[position]; !exists {
				return errorsf("primary key positions in %s must be contiguous from 1", table.Name)
			}
		}
		if autoIncrementColumns > 1 {
			return errorsf("table %s has multiple AUTO_INCREMENT columns", table.Name)
		}
		indexes := map[string]struct{}{}
		for _, index := range table.Indexes {
			if !validIdentifier(index.Name) {
				return errorsf("invalid index name %s.%q", table.Name, index.Name)
			}
			if _, exists := indexes[index.Name]; exists {
				return errorsf("duplicate index %s.%s", table.Name, index.Name)
			}
			indexes[index.Name] = struct{}{}
			for _, column := range index.Columns {
				if _, exists := columns[column]; !exists {
					return errorsf("index %s references missing %s.%s", index.Name, table.Name, column)
				}
			}
		}
	}
	for _, table := range m.Tables {
		foreignKeyGroups := map[string][]ForeignKey{}
		for _, fk := range table.ForeignKeys {
			if _, exists := tables[fk.RefTable]; !exists {
				return errorsf("foreign key %s references missing table %s", fk.Name, fk.RefTable)
			}
			if _, exists := tableColumns[table.Name][fk.Column]; !exists {
				return errorsf("foreign key %s references missing local column %s.%s", fk.Name, table.Name, fk.Column)
			}
			if _, exists := tableColumns[fk.RefTable][fk.RefColumn]; !exists {
				return errorsf("foreign key %s references missing column %s.%s", fk.Name, fk.RefTable, fk.RefColumn)
			}
			foreignKeyGroups[fk.Name] = append(foreignKeyGroups[fk.Name], fk)
		}
		for name, group := range foreignKeyGroups {
			first := group[0]
			refNames := make([]string, len(group))
			for i, fk := range group {
				if fk.RefTable != first.RefTable || normalizedForeignKeyAction(fk.OnUpdate) != normalizedForeignKeyAction(first.OnUpdate) ||
					normalizedForeignKeyAction(fk.OnDelete) != normalizedForeignKeyAction(first.OnDelete) {
					return errorsf("foreign key group %s.%s has inconsistent target or actions", table.Name, name)
				}
				if !validForeignKeyAction(fk.OnUpdate) || !validForeignKeyAction(fk.OnDelete) {
					return errorsf("foreign key %s.%s has unsupported action", table.Name, name)
				}
				localColumn := findColumn(table, fk.Column)
				refColumn := findColumn(tablesByName[fk.RefTable], fk.RefColumn)
				if localColumn.Kind != refColumn.Kind {
					return errorsf("foreign key %s.%s has incompatible column kinds %s and %s", table.Name, name, localColumn.Kind, refColumn.Kind)
				}
				refNames[i] = fk.RefColumn
			}
			if !isUniqueColumnGroup(tablesByName[first.RefTable], refNames) {
				return errorsf("foreign key %s.%s references non-unique columns %s.%v", table.Name, name, first.RefTable, refNames)
			}
		}
	}
	return nil
}

func validForeignKeyAction(value string) bool {
	switch normalizedForeignKeyAction(value) {
	case "CASCADE", "RESTRICT", "SET NULL", "SET DEFAULT", "NO ACTION":
		return true
	default:
		return false
	}
}

func isUniqueColumnGroup(table Table, names []string) bool {
	if sameColumnNames(names, table.PrimaryKey()) {
		return true
	}
	for _, index := range table.Indexes {
		if index.Unique && sameStringColumns(names, index.Columns) {
			return true
		}
	}
	return false
}

func sameStringColumns(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (m Manifest) AuthoritativeTables() []Table {
	var result []Table
	for _, table := range m.Tables {
		if table.Class == ClassAuthoritative {
			result = append(result, table)
		}
	}
	return result
}

func (t Table) PrimaryKey() []Column {
	var columns []Column
	for _, column := range t.Columns {
		if column.PrimaryKeyPosition > 0 {
			columns = append(columns, column)
		}
	}
	sort.Slice(columns, func(i, j int) bool { return columns[i].PrimaryKeyPosition < columns[j].PrimaryKeyPosition })
	return columns
}

func cloneManifest(source Manifest) Manifest {
	encoded, _ := json.Marshal(source)
	var result Manifest
	_ = json.Unmarshal(encoded, &result)
	return result
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r == '_' || r == '$' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func errorsf(format string, args ...any) error { return fmt.Errorf(format, args...) }

func normalizedDefault(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
