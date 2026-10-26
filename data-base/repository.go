package data_base

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

type Where struct {
	Column string
	Value  any
}

func structData[T any](data T) ([]string, []any, string, error) {
	value := reflect.ValueOf(data)

	if value.Kind() != reflect.Struct {
		return nil, nil, "", fmt.Errorf("data must be struct")
	}

	table := strings.ToLower(reflect.TypeOf(data).Name())

	if named, ok := any(data).(interface{ TableName() string }); ok {
		table = named.TableName()
	}

	typeOf := value.Type()

	columns := make([]string, 0, value.NumField())
	values := make([]any, 0, value.NumField())

	for i := 0; i < value.NumField(); i++ {
		field := typeOf.Field(i)

		column := field.Tag.Get("db")

		if column == "" || column == "-" {
			continue
		}

		columns = append(columns, column)
		values = append(values, value.Field(i).Interface())
	}

	return columns, values, table, nil
}

func structDataInsert[T any](data T) ([]string, []any, string, error) {
	value := reflect.ValueOf(data)

	if value.Kind() != reflect.Struct {
		return nil, nil, "", fmt.Errorf("data must be struct")
	}

	table := strings.ToLower(reflect.TypeOf(data).Name())

	if named, ok := any(data).(interface{ TableName() string }); ok {
		table = named.TableName()
	}

	typeOf := value.Type()
	columns := make([]string, 0, value.NumField())
	values := make([]any, 0, value.NumField())

	for i := 0; i < value.NumField(); i++ {
		field := typeOf.Field(i)
		column := field.Tag.Get("db")

		if column == "" || column == "-" {
			continue
		}

		if value.Field(i).IsZero() {
			continue
		}

		columns = append(columns, column)
		values = append(values, value.Field(i).Interface())
	}

	return columns, values, table, nil
}

func scanStruct[T any](rows *sql.Rows) (T, error) {
	var result T

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return result, err
		}

		return result, sql.ErrNoRows
	}

	value := reflect.ValueOf(&result).Elem()
	typeOf := value.Type()

	columns, err := rows.Columns()
	if err != nil {
		return result, err
	}

	fields := make(map[string]int)

	for i := 0; i < value.NumField(); i++ {
		field := typeOf.Field(i)
		column := field.Tag.Get("db")

		if column == "" || column == "-" {
			continue
		}

		fields[column] = i
	}

	dest := make([]any, len(columns))
	ignored := make([]any, len(columns))

	for i, column := range columns {
		index, ok := fields[column]

		if !ok {
			dest[i] = &ignored[i]
			continue
		}

		field := value.Field(index)

		if !field.CanAddr() {
			return result, fmt.Errorf("field %q cannot be addressed", column)
		}

		dest[i] = field.Addr().Interface()
	}

	if err := rows.Scan(dest...); err != nil {
		return result, err
	}

	return result, rows.Err()
}

type Database[T comparable] interface {
	Insert(data T) (*T, error)
	Update(data T) (*T, error)
	Delete(where T) error
	Find(where Where) (*T, error)
	First() (*T, error)
}

func assignments(columns []string) string {
	result := make([]string, len(columns))

	for i, column := range columns {
		result[i] = fmt.Sprintf("%s = $%d", column, i+1)
	}

	return strings.Join(result, ", ")
}

type database[T comparable] struct {
	db  *sql.DB
	ctx context.Context
}

func buildConnString() string {
	password := os.Getenv("DB_PASSWORD")
	host := os.Getenv("DB_HOST")
	port := os.Getenv("DB_PORT")
	databaseName := os.Getenv("DB_NAME")
	user := os.Getenv("DB_USER")

	parameters := url.Values{}

	switch strings.ToLower(os.Getenv("SSL_MODE")) {
	case "false", "0", "disable", "":
		parameters.Set("sslmode", "disable")
	default:
		parameters.Set("sslmode", "require")
	}

	connection := url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(host, port),
		Path:     "/" + databaseName,
		RawQuery: parameters.Encode(),
	}

	return connection.String()
}

// NewConnection отдаёт *sql.DB для прямых SQL-запросов (музыкальный трек-лист).
func NewConnection(ctx context.Context) (*sql.DB, error) {
	conn, err := sql.Open("postgres", buildConnString())
	if err != nil {
		return nil, err
	}

	if err := conn.PingContext(ctx); err != nil {
		return nil, err
	}

	return conn, nil
}

func NewRepository[T comparable](ctx context.Context) (Database[T], error) {
	connStr := buildConnString()

	conn, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	if err := conn.PingContext(ctx); err != nil {
		return nil, err
	}
	return &database[T]{ctx: ctx, db: conn}, nil
}
func Migrate() error {
	db, err := sql.Open("postgres", buildConnString())
	if err != nil {
		return err
	}

	defer func() {
		_ = db.Close()
	}()

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS migrations (
			id serial PRIMARY KEY,
			name varchar(255) NOT NULL,
			run_on timestamp NOT NULL DEFAULT now()
		)
	`); err != nil {
		return err
	}

	applied := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM migrations`)
	if err != nil {
		return err
	}

	for rows.Next() {
		var name string

		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()

			return err
		}

		applied[name] = true
	}

	_ = rows.Close()

	migrationsPath, err := filepath.Abs("migrations")
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(migrationsPath)
	if err != nil {
		return err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), ".sql")

		if applied[name] {
			continue
		}

		body, err := os.ReadFile(filepath.Join(migrationsPath, entry.Name()))
		if err != nil {
			return err
		}

		transaction, err := db.Begin()
		if err != nil {
			return err
		}

		if _, err := transaction.Exec(string(body)); err != nil {
			_ = transaction.Rollback()

			return fmt.Errorf("migration %s: %w", name, err)
		}

		if _, err := transaction.Exec(
			`INSERT INTO migrations (name, run_on) VALUES ($1, $2)`,
			name,
			time.Now(),
		); err != nil {
			_ = transaction.Rollback()

			return fmt.Errorf("migration %s: %w", name, err)
		}

		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}

	return nil
}

func (db *database[T]) Insert(data T) (*T, error) {
	tx, err := db.db.BeginTx(db.ctx, &sql.TxOptions{Isolation: sql.LevelReadUncommitted})
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = tx.Rollback()
	}()

	columns, values, table, err := structDataInsert(data)
	if err != nil {
		return nil, err
	}

	placeholders := make([]string, len(values))
	for i := range values {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) RETURNING *",
		table, strings.Join(columns, ", "),
		strings.Join(placeholders, ", "))

	rows, err := tx.QueryContext(db.ctx, query, values...)
	if err != nil {
		return nil, err
	}

	res, err := scanStruct[T](rows)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &res, nil
}

func (db *database[T]) Update(data T) (*T, error) {
	tx, err := db.db.BeginTx(db.ctx, &sql.TxOptions{Isolation: sql.LevelReadUncommitted})
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = tx.Rollback()
	}()

	columns, values, table, err := structData(data)
	if err != nil {
		return nil, err
	}

	idIndex := -1

	for index, column := range columns {
		if column == "id" {
			idIndex = index

			break
		}
	}

	if idIndex < 0 {
		return nil, fmt.Errorf("update requires id column")
	}

	id := values[idIndex]
	setColumns := make([]string, 0, len(columns)-1)
	setValues := make([]any, 0, len(values)-1)

	for index, column := range columns {
		if index == idIndex {
			continue
		}

		setColumns = append(setColumns, column)
		setValues = append(setValues, values[index])
	}

	assigns := make([]string, len(setColumns))

	for index, column := range setColumns {
		assigns[index] = fmt.Sprintf("%s = $%d", column, index+2)
	}

	query := fmt.Sprintf(
		"UPDATE %s SET %s WHERE id = $1 RETURNING *",
		table,
		strings.Join(assigns, ", "),
	)
	args := append([]any{id}, setValues...)

	rows, err := tx.QueryContext(db.ctx, query, args...)
	if err != nil {
		return nil, err
	}

	res, err := scanStruct[T](rows)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &res, nil
}

func (db *database[T]) Delete(where T) error {
	tx, err := db.db.BeginTx(db.ctx, &sql.TxOptions{Isolation: 1})

	defer func() {
		err = tx.Rollback()
	}()
	if err != nil {
		return err
	}

	_, values, table, err := structData(where)
	if err != nil {
		return err
	}

	query := fmt.Sprintf("DELETE FROM %s WHERE id = $1", table)
	_, err = tx.ExecContext(db.ctx, query, values...)
	if err != nil {
		return err
	}
	err = tx.Commit()
	if err != nil {
		return err
	}
	return err
}

// First возвращает первую запись таблицы (или sql.ErrNoRows).
func (db *database[T]) First() (*T, error) {
	var res T

	columns, _, table, err := structData(res)
	if err != nil {
		return nil, err
	}

	query := fmt.Sprintf("SELECT %s FROM %s LIMIT 1", strings.Join(columns, ", "), table)

	rows, err := db.db.QueryContext(db.ctx, query)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = rows.Close()
	}()

	result, err := scanStruct[T](rows)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

func (db *database[T]) Find(where Where) (*T, error) {
	var res T

	columns, _, table, err := structData(res)
	if err != nil {
		return nil, err
	}

	query := fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s = $1 LIMIT 1",
		strings.Join(columns, ", "),
		table,
		where.Column,
	)

	rows, err := db.db.QueryContext(db.ctx, query, where.Value)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = rows.Close()
	}()

	result, err := scanStruct[T](rows)
	if err != nil {
		return nil, err
	}

	return &result, nil
}
