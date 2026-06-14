import { describe, expect, test } from "bun:test";
import { assertReadOnly } from "../src/datasources/validate";

const allowed = [
    "SELECT * FROM users LIMIT 100",
    "select id, name from accounts where active = true",
    "WITH t AS (SELECT 1 AS n) SELECT * FROM t",
    "EXPLAIN SELECT * FROM orders",
    "EXPLAIN ANALYZE SELECT * FROM orders", // ANALYZE here is legitimate
    "SHOW TABLES",
    "DESCRIBE users",
    "VALUES (1), (2)",
    "TABLE users",
    "  SELECT 1 ;", // single trailing semicolon ok
    "SELECT 'please delete everything' AS note", // mutation word only in a string literal
    "SELECT updated_at, dropped_at FROM events", // mutation word as a column substring
    "(SELECT 1)",
];

const rejected = [
    "INSERT INTO users (name) VALUES ('x')",
    "UPDATE users SET name = 'x'",
    "DELETE FROM users",
    "DROP TABLE users",
    "ALTER TABLE users ADD COLUMN x int",
    "CREATE TABLE t (id int)",
    "TRUNCATE users",
    "GRANT ALL ON users TO bob",
    "WITH x AS (DELETE FROM t RETURNING *) SELECT * FROM x", // CTE mutation disguised as a read
    "SELECT 1; DROP TABLE users", // stacked statements
    "SELECT * FROM users INTO OUTFILE '/tmp/x'", // MySQL file write
    "COPY users TO PROGRAM 'sh -c rm'", // PG program exec
    "SELECT pg_read_file('/etc/passwd')",
    "/* sneaky */ DELETE FROM users",
    "DeLeTe FROM users", // case-insensitive
    "CALL do_something()",
    "",
    "   ",
];

describe("assertReadOnly", () => {
    for (const q of allowed) {
        test(`allows: ${q}`, () => {
            expect(() => assertReadOnly(q)).not.toThrow();
        });
    }
    for (const q of rejected) {
        test(`rejects: ${q}`, () => {
            expect(() => assertReadOnly(q)).toThrow();
        });
    }
});
