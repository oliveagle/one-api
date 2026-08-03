#!/usr/bin/env python3
"""
Migrate Homarr data from PostgreSQL to SQLite.
Usage: cat pg_dump_data.sql | python3 migrate_homarr_to_sqlite.py homarr.db
"""
import re
import sqlite3
import sys

def pg_type_to_sqlite(pg_type):
    """Convert PostgreSQL column type to SQLite-compatible type."""
    t = pg_type.strip().lower()
    
    # Remove NOT NULL, DEFAULT, etc.
    t = re.sub(r'\s+not\s+null.*', '', t, flags=re.IGNORECASE)
    t = re.sub(r'\s+default\s+.*', '', t, flags=re.IGNORECASE)
    
    # Remove constraints like PRIMARY KEY, REFERENCES, UNIQUE
    t = re.sub(r'\s+primary\s+key.*', '', t, flags=re.IGNORECASE)
    t = re.sub(r'\s+references\s+.*', '', t, flags=re.IGNORECASE)
    t = re.sub(r'\s+unique.*', '', t, flags=re.IGNORECASE)
    
    t = t.strip()
    
    if t.startswith('character varying') or t.startswith('varchar'):
        return 'TEXT'
    elif t == 'text':
        return 'TEXT'
    elif t == 'boolean' or t == 'bool':
        return 'INTEGER'
    elif t.startswith('integer') or t == 'int' or t == 'serial' or t.startswith('bigint') or t == 'bigserial':
        return 'INTEGER'
    elif t.startswith('timestamp') or t.startswith('date') or t.startswith('time'):
        return 'TEXT'
    elif t == 'json' or t == 'jsonb':
        return 'TEXT'
    elif t.startswith('double') or t.startswith('float') or t.startswith('numeric') or t.startswith('decimal') or t == 'real':
        return 'REAL'
    elif t.startswith('bytea') or t.startswith('blob'):
        return 'BLOB'
    elif t.startswith('smallint'):
        return 'INTEGER'
    elif t.startswith('oid'):
        return 'INTEGER'
    else:
        return 'TEXT'  # fallback

def parse_create_table(sql):
    """Parse CREATE TABLE statement and extract table name and columns."""
    # Remove schema prefix "public."
    sql = re.sub(r'\bpublic\.', '', sql)
    # Remove drizzle schema
    sql = re.sub(r'\bdrizzle\.', '', sql)
    
    # Extract table name
    m = re.match(r'CREATE\s+TABLE\s+(?:"([^"]+)"|(\w+))\s*\(', sql, re.IGNORECASE)
    if not m:
        return None, []
    table_name = m.group(1) or m.group(2)
    
    # Extract columns (between first ( and last ))
    paren_depth = 0
    start_idx = -1
    for i, ch in enumerate(sql):
        if ch == '(':
            if paren_depth == 0:
                start_idx = i + 1
            paren_depth += 1
        elif ch == ')':
            paren_depth -= 1
            if paren_depth == 0:
                body = sql[start_idx:i]
                break
    
    if start_idx < 0:
        return table_name, []
    
    # Parse columns (split by comma, respecting nested parens)
    columns = []
    current = ''
    depth = 0
    for ch in body:
        if ch in '(,' and depth == 0:
            if ch == ',':
                columns.append(current.strip())
                current = ''
            else:
                current += ch
                depth += 1
        elif ch == '(':
            current += ch
            depth += 1
        elif ch == ')':
            current += ch
            depth -= 1
        else:
            current += ch
    if current.strip():
        columns.append(current.strip())
    
    # Filter to only column definitions (not constraints, keys, etc.)
    col_defs = []
    for col in columns:
        col = col.strip()
        if not col:
            continue
        # Skip constraints, keys, etc.
        if re.match(r'^(CONSTRAINT|PRIMARY|FOREIGN|UNIQUE|CHECK|INDEX|EXCLUDE|REFERENCES)', col, re.IGNORECASE):
            continue
        # Must have a column name followed by a type
        if re.match(r'^["\w]+\s+\w+', col, re.IGNORECASE):
            col_defs.append(col)
    
    return table_name, col_defs

def create_sqlite_schema(pg_schema_sql, db_path):
    """Create SQLite tables from PostgreSQL CREATE TABLE statements."""
    conn = sqlite3.connect(db_path)
    cur = conn.cursor()
    
    # Enable WAL mode
    cur.execute("PRAGMA journal_mode=WAL")
    
    tables_created = []
    
    for stmt in re.split(r';\s*\n', pg_schema_sql):
        stmt = stmt.strip()
        if not stmt or stmt.startswith('--'):
            continue
        
        if re.match(r'CREATE\s+TABLE', stmt, re.IGNORECASE):
            table_name, columns = parse_create_table(stmt)
            if table_name and columns:
                sqlite_cols = []
                for col in columns:
                    # Parse: column_name type [constraints]
                    m = re.match(r'(\s*"[^"]+"|\s*\w+)\s+(.*)', col)
                    if m:
                        col_name = m.group(1).strip()
                        col_type_part = m.group(2).strip()
                        
                        # Check for PRIMARY KEY
                        is_pk = 'primary key' in col_type_part.lower()
                        is_not_null = 'not null' in col_type_part.lower()
                        
                        # Convert type
                        sqlite_type = pg_type_to_sqlite(col_type_part)
                        
                        # Build SQLite column def
                        sqlite_col = f"{col_name} {sqlite_type}"
                        if is_pk:
                            sqlite_col += " PRIMARY KEY"
                        if is_not_null:
                            sqlite_col += " NOT NULL"
                        
                        sqlite_cols.append(sqlite_col)
                
                if sqlite_cols:
                    cols_joined = ',\n  '.join(sqlite_cols)
                    create_sql = 'CREATE TABLE IF NOT EXISTS "' + table_name + '" (\n  ' + cols_joined + '\n)'
                    try:
                        cur.execute(create_sql)
                        tables_created.append(table_name)
                        print(f"  ✅ Created table: {table_name}")
                    except Exception as e:
                        print(f"  ❌ Failed to create {table_name}: {e}")
                        print(f"     SQL: {create_sql[:200]}")
    
    conn.commit()
    conn.close()
    return tables_created

def convert_pg_value(value, col_type_hint='TEXT'):
    """Convert a PostgreSQL value literal to SQLite-compatible format."""
    if value == 'NULL':
        return 'NULL'
    
    # If it's a string literal (single-quoted)
    if value.startswith("'") and (value.endswith("'") or value.endswith("'::text") or value.endswith("'::character varying")):
        # Remove type cast suffixes
        v = re.sub(r"'::\w+(?:\(\d+\))?$", "'", value)
        # Escape single quotes for SQLite (already escaped in PG format with '')
        return v
    
    # Boolean
    if value.lower() in ('true', 'false'):
        return '1' if value.lower() == 'true' else '0'
    
    # Default expressions to skip
    if value.lower() in ('now()', 'current_timestamp', 'current_date'):
        return "'NOW'"  # placeholder, will be set at runtime
    
    # Numbers and other literals
    return value

def parse_insert_statement(line):
    """Parse a single INSERT INTO statement and return (schema, table, columns, values_list)."""
    # Remove schema prefix
    line = re.sub(r'\bpublic\.', '', line)
    line = re.sub(r'\bdrizzle\.', '', line)
    
    m = re.match(r"INSERT\s+INTO\s+(?:\"([^\"]+)\"|(\w+))\s*(?:\(([^)]*)\))?\s*VALUES\s*(.*);",
                 line, re.IGNORECASE | re.DOTALL)
    if not m:
        return None
    
    table_name = m.group(1) or m.group(2)
    columns_str = m.group(3)
    values_str = m.group(4)
    
    if columns_str:
        columns = [c.strip().strip('"') for c in columns_str.split(',')]
    else:
        columns = None
    
    # Parse values (handle nested parens for composite types)
    values_list = []
    current_val = ''
    depth = 0
    for ch in values_str:
        if ch == '(' and depth == 0:
            current_val = ''
            depth = 1
        elif ch == '(' and depth > 0:
            current_val += ch
            depth += 1
        elif ch == ')' and depth == 1:
            values_list.append(current_val.strip())
            current_val = ''
            depth = 0
        elif ch == ')' and depth > 1:
            current_val += ch
            depth -= 1
        elif depth > 0:
            current_val += ch
    
    return table_name, columns, values_list

def import_data(pg_data_lines, db_path, tables):
    """Import data from pg_dump output into SQLite."""
    conn = sqlite3.connect(db_path)
    cur = conn.cursor()
    
    # Disable foreign keys during import
    cur.execute("PRAGMA foreign_keys=OFF")
    
    total_rows = 0
    for line in pg_data_lines:
        line = line.strip()
        if not line or line.startswith('--') or line.startswith('SET ') or line.startswith('SELECT ') or line.startswith('\\restrict'):
            continue
        
        if line.startswith('INSERT INTO'):
            result = parse_insert_statement(line)
            if result is None:
                continue
            table_name, columns, values_list = result
            
            if not values_list:
                continue
            
            if table_name not in tables:
                continue
            
            col_names_str = ','.join(f'"{c}"' for c in columns) if columns else ''
            
            for values_str in values_list:
                # Parse individual values
                vals = []
                current = ''
                depth = 0
                in_str = False
                for ch in values_str:
                    if ch == "'" and not in_str:
                        in_str = True
                        current += ch
                    elif ch == "'" and in_str:
                        in_str = False
                        current += ch
                    elif ch == ',' and depth == 0 and not in_str:
                        vals.append(current.strip())
                        current = ''
                    elif ch == '(' and not in_str:
                        depth += 1
                        current += ch
                    elif ch == ')' and not in_str:
                        depth -= 1
                        current += ch
                    else:
                        current += ch
                if current.strip():
                    vals.append(current.strip())
                
                # Convert values
                converted = []
                placeholders = []
                for v in vals:
                    if v == 'NULL':
                        converted.append(None)
                        placeholders.append('?')
                    elif v.startswith("'") and (v.endswith("'") or "'::" in v):
                        # Extract the string content
                        sv = v
                        # Handle type casts like 'value'::text
                        sv = re.sub(r"'::\w+(?:\(\d+\))?$", "'", sv)
                        # Unescape PG single quotes
                        content = sv[1:-1].replace("''", "'")
                        converted.append(content)
                        placeholders.append('?')
                    elif v.lower() == 'true':
                        converted.append(1)
                        placeholders.append('?')
                    elif v.lower() == 'false':
                        converted.append(0)
                        placeholders.append('?')
                    else:
                        try:
                            if '.' in v:
                                converted.append(float(v))
                            else:
                                converted.append(int(v))
                        except:
                            converted.append(v)
                        placeholders.append('?')
                
                if col_names_str:
                    sql = f'INSERT OR REPLACE INTO "{table_name}" ({col_names_str}) VALUES ({",".join(placeholders)})'
                else:
                    sql = f'INSERT OR REPLACE INTO "{table_name}" VALUES ({",".join(placeholders)})'
                
                try:
                    cur.execute(sql, converted)
                    total_rows += 1
                except Exception as e:
                    print(f"  ❌ Failed to insert into {table_name}: {e}")
                    print(f"     Values: {converted[:5]}...")
                    conn.rollback()
                    # Try without OR REPLACE
                    try:
                        sql_no_replace = sql.replace('INSERT OR REPLACE', 'INSERT')
                        cur.execute(sql_no_replace, converted)
                        total_rows += 1
                    except:
                        pass
    
    conn.commit()
    conn.close()
    print(f"\n  ✅ Imported {total_rows} rows total")
    return total_rows

def main():
    if len(sys.argv) < 2:
        print("Usage: python3 migrate_homarr_to_sqlite.py <output.db>")
        print("       cat pg_dump_data.sql | python3 migrate_homarr_to_sqlite.py <output.db>")
        sys.exit(1)
    
    db_path = sys.argv[1]
    
    # Read all lines from stdin
    all_lines = list(sys.stdin)
    
    # Phase 1: Extract all CREATE TABLE statements
    print("=== Creating SQLite database schema ===")
    schema_lines = []
    in_create = False
    for line in all_lines:
        if line.strip().startswith('CREATE TABLE'):
            in_create = True
            schema_lines.append(line)
        elif in_create:
            schema_lines.append(line)
            if line.strip() == ');':
                in_create = False
    
    schema_sql = ''.join(schema_lines)
    tables = create_sqlite_schema(schema_sql, db_path)
    print(f"\n  Created {len(tables)} tables")
    
    # Phase 2: Import data from INSERT statements
    print("\n=== Importing data ===")
    data_lines = [l for l in all_lines if l.strip().startswith('INSERT INTO')]
    import_data(data_lines, db_path, tables)
    
    print("\n=== Verifying ===")
    conn = sqlite3.connect(db_path)
    cur = conn.cursor()
    cur.execute("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
    tables_in_db = cur.fetchall()
    for t in tables_in_db:
        cur.execute(f'SELECT COUNT(*) FROM "{t[0]}"')
        count = cur.fetchone()[0]
        if count > 0:
            print(f"  📊 {t[0]}: {count} rows")
    conn.close()
    
    print(f"\n✅ Migration complete! Database saved to: {db_path}")

if __name__ == '__main__':
    main()
