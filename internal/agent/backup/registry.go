// Package backup implements plan-kind adapters.
package backup

// For returns the adapter for the given kind, or false if unknown.
func For(kind string) (Adapter, bool) {
	switch kind {
	case "filesystem":
		return &FilesystemAdapter{}, true
	case "postgresql":
		return &PostgreSQLAdapter{}, true
	case "mysql":
		return &MySQLAdapter{}, true
	case "mongodb":
		return &MongoDBAdapter{}, true
	case "sqlite":
		return &SQLiteAdapter{}, true
	default:
		return nil, false
	}
}

// Kinds returns all supported plan kinds.
func Kinds() []string {
	return []string{"filesystem", "postgresql", "mysql", "mongodb", "sqlite"}
}