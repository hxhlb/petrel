package orm

import (
	"bytes"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"unicode"

	"github.com/davygeek/log"
	"github.com/juju/errors"
)

var (
	//ErrNotFound db not found.
	ErrNotFound = errors.New("not found")
)

//Stmt db stmt.
type Stmt struct {
	table  string
	where  string
	sort   string
	order  string
	group  string
	offset int
	limit  int
	db     *sql.DB
}

//NewStmt new db stmt.
func NewStmt(db *sql.DB, table string) *Stmt {
	return &Stmt{
		table: table,
		db:    db,
	}
}

//Where 添加查询条件
func (s *Stmt) Where(f string, args ...interface{}) *Stmt {
	if len(args) > 0 {
		s.where = fmt.Sprintf(f, args...)
	} else {
		s.where = f
	}
	return s
}

//Sort 添加sort
func (s *Stmt) Sort(sort string) *Stmt {
	s.sort = sort
	return s
}

//Group 添加group by.
func (s *Stmt) Group(group string) *Stmt {
	s.group = group
	return s
}

//Order 添加order
func (s *Stmt) Order(order string) *Stmt {
	s.order = order
	return s
}

//Offset 添加offset
func (s *Stmt) Offset(offset int) *Stmt {
	s.offset = offset
	return s
}

//Limit 添加limit
func (s *Stmt) Limit(limit int) *Stmt {
	s.limit = limit
	return s
}

//SQLQueryBuilder build sql query.
func (s *Stmt) SQLQueryBuilder(result interface{}) (string, error) {
	rt := reflect.TypeOf(result)
	if rt.Kind() != reflect.Ptr {
		return "", fmt.Errorf("result type must be ptr, recv:%v", rt.Kind())
	}

	//ptr
	rt = rt.Elem()
	if rt.Kind() == reflect.Slice {
		rt = rt.Elem()
	} else {
		//只查一条加上limit 1
		s.limit = 1
	}

	//empty struct
	if rt.NumField() == 0 {
		return "", fmt.Errorf("result not found field")
	}

	return s.SQLQuery(rt), nil
}

//SQLCondition where, order, limit
func (s *Stmt) SQLCondition(bs *bytes.Buffer) {
	if s.where != "" {
		fmt.Fprintf(bs, " where %s", s.where)
	}

	if s.sort != "" {
		fmt.Fprintf(bs, " order by %s", s.sort)
		if s.order != "" {
			fmt.Fprintf(bs, " %s", s.order)
		}
	}

	if s.group != "" {
		fmt.Fprintf(bs, " group by %s", s.group)
	}

	if s.limit > 0 {
		bs.WriteString(" limit ")
		if s.offset > 0 {
			fmt.Fprintf(bs, "%d,", s.offset)
		}
		fmt.Fprintf(bs, "%d", s.limit)
	}
}

// SQLCount 根据条件及结构生成查询sql
func (s *Stmt) SQLCount() string {
	bs := bytes.NewBufferString("select count(*) from ")
	bs.WriteString(s.table)

	s.SQLCondition(bs)

	sql := bs.String()
	log.Debugf("sql:%v", sql)
	return sql
}

// SQLQuery 根据条件及结构生成查询sql
func (s *Stmt) SQLQuery(rt reflect.Type) string {
	firstTable := strings.Split(s.table, ",")[0]

	bs := bytes.NewBufferString("select ")

	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" && !f.Anonymous { // unexported
			continue
		}
		name := f.Tag.Get("db")
		if name == "" {
			name = FieldEscape(f.Name)
		}
		if !strings.Contains(name, ".") {
			fmt.Fprintf(bs, "%s.", firstTable)
		}
		fmt.Fprintf(bs, "%s, ", name)
	}

	bs.Truncate(bs.Len() - 2)
	fmt.Fprintf(bs, " from %s", s.table)

	s.SQLCondition(bs)

	sql := bs.String()
	log.Debugf("sql:%v", sql)
	return sql
}

// Query 根据传入的result结构，生成查询sql，并返回执行结果， result 必需是一个指向切片的指针.
func (s *Stmt) Query(result interface{}) error {
	rt := reflect.TypeOf(result)

	if rt.Kind() != reflect.Ptr {
		return fmt.Errorf("result type must be ptr, recv:%v", rt.Kind())
	}

	//ptr
	rt = rt.Elem()
	if rt.Kind() == reflect.Slice {
		rt = rt.Elem()
	} else {
		//只查一条加上limit 1
		s.limit = 1
	}

	//empty struct
	if rt.NumField() == 0 {
		return fmt.Errorf("result not found field")
	}

	sql := s.SQLQuery(rt)

	rows, err := s.db.Query(sql)
	if err != nil {
		return errors.Trace(err)
	}
	defer rows.Close()

	rv := reflect.ValueOf(result).Elem()

	for rows.Next() {
		var refs []interface{}
		obj := reflect.New(rt)

		for i := 0; i < obj.Elem().NumField(); i++ {
			f := rt.Field(i)
			if f.PkgPath != "" && !f.Anonymous { // unexported
				continue
			}
			refs = append(refs, obj.Elem().Field(i).Addr().Interface())
		}

		if err = rows.Scan(refs...); err != nil {
			return errors.Trace(err)
		}

		if rv.Kind() == reflect.Struct {
			reflect.ValueOf(result).Elem().Set(reflect.ValueOf(obj.Elem().Interface()))
			log.Debugf("result %v", result)
			return nil
		}

		rv = reflect.Append(rv, obj.Elem())
	}

	if rv.Kind() == reflect.Struct || rv.Len() == 0 {
		return errors.Trace(ErrNotFound)
	}

	reflect.ValueOf(result).Elem().Set(reflect.ValueOf(rv.Interface()))
	log.Debugf("result %v", result)

	return nil
}

//Count 查询总数.
func (s *Stmt) Count() (int64, error) {
	rows, err := s.db.Query(s.SQLCount())
	if err != nil {
		return 0, errors.Trace(err)
	}
	defer rows.Close()

	rows.Next()

	var n int64
	if err = rows.Scan(&n); err != nil {
		return 0, errors.Trace(err)
	}

	return n, nil
}

//SQLInsert 添加数据
func (s *Stmt) SQLInsert(rt reflect.Type, rv reflect.Value) (sql string, refs []interface{}) {
	bs := bytes.NewBufferString("insert into ")
	bs.WriteString(s.table)
	bs.WriteString(" (")

	dbs := bytes.NewBufferString(") values (")

	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).PkgPath != "" && !rt.Field(i).Anonymous { // unexported
			continue
		}
		def := rt.Field(i).Tag.Get("db_default")
		if def == "auto" {
			continue
		}
		name := rt.Field(i).Tag.Get("db")
		if name == "" {
			name = FieldEscape(rt.Field(i).Name)
		}

		bs.WriteString(name)
		bs.WriteString(", ")

		if def != "" {
			dbs.WriteString(def)
			dbs.WriteString(", ")
			continue
		}

		dbs.WriteString("?, ")
		refs = append(refs, rv.Field(i).Interface())
	}

	bs.Truncate(bs.Len() - 2)
	dbs.Truncate(dbs.Len() - 2)

	bs.WriteString(dbs.String())

	bs.WriteString(") ")
	sql = bs.String()
	return
}

//FieldEscape 转换为小写下划线分隔
func FieldEscape(k string) string {
	buf := []byte{}
	up := true
	for _, c := range k {
		if unicode.IsUpper(c) {
			if !up {
				buf = append(buf, '_')
			}
			c += 32
			up = true
		} else {
			up = false
		}

		buf = append(buf, byte(c))
	}
	return string(buf)
}

// SQLUpdate 根据条件及结构生成update sql
func (s *Stmt) SQLUpdate(rt reflect.Type, rv reflect.Value) (sql string, refs []interface{}) {
	bs := bytes.NewBufferString("")
	fmt.Fprintf(bs, "update `%s` set ", s.table)

	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).PkgPath != "" && !rt.Field(i).Anonymous { // unexported
			continue
		}
		def := rt.Field(i).Tag.Get("db_default")
		if def == "auto" {
			continue
		}
		name := rt.Field(i).Tag.Get("db")
		if name == "" {
			name = FieldEscape(rt.Field(i).Name)
		}

		fmt.Fprintf(bs, "`%s`=", name)

		if def != "" {
			fmt.Fprintf(bs, "%s, ", def)
			continue
		}

		bs.WriteString("?, ")
		refs = append(refs, rv.Field(i).Interface())
	}

	bs.Truncate(bs.Len() - 2)

	s.SQLCondition(bs)

	sql = bs.String()
	log.Debugf("sql:%v", sql)
	return
}

//Update sql update db.
func (s *Stmt) Update(data interface{}) (int64, error) {
	rt := reflect.TypeOf(data)
	rv := reflect.ValueOf(data)

	if rt.Kind() == reflect.Ptr {
		rt = rt.Elem()
		rv = rv.Elem()
	}

	if rt.NumField() == 0 {
		return 0, fmt.Errorf("data not found field")
	}
	sql, refs := s.SQLUpdate(rt, rv)
	r, err := s.db.Exec(sql, refs...)
	if err != nil {
		return 0, errors.Trace(err)
	}
	return r.RowsAffected()
}

//Insert sql update db.
func (s *Stmt) Insert(data interface{}) (int64, error) {
	rt := reflect.TypeOf(data)
	rv := reflect.ValueOf(data)

	if rt.Kind() == reflect.Ptr {
		rt = rt.Elem()
		rv = rv.Elem()
	}

	if rt.NumField() == 0 {
		return 0, fmt.Errorf("data not found field")
	}

	sql, refs := s.SQLInsert(rt, rv)
	r, err := s.db.Exec(sql, refs...)
	if err != nil {
		return 0, errors.Trace(err)
	}

	return r.LastInsertId()
}
