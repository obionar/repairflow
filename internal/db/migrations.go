package db

// Embedded, forward-only schema migrations. Never rewrite a released
// migration: add a new one. See docs/ARCHITECTURE.md (Schema).

type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{
	{1, "initial_domain", schemaV1},
	{2, "workflow_completion", schemaV2},
	{3, "parts_estimates", schemaV3},
	{4, "attachments", schemaV4},
	{5, "request_idempotency", schemaV5},
	{6, "users_sessions", schemaV6},
	{7, "contact_device_parity", schemaV7},
	{8, "drop_legacy_ticket_id", schemaV8},
	{9, "repair_customer_optional", schemaV9},
}

// LatestVersion returns the highest known migration version.
func LatestVersion() int {
	if len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].version
}

const schemaV8 = `
DROP INDEX idx_repairs_legacy_id;
ALTER TABLE repairs DROP COLUMN legacy_id;
`

// schemaV9 rebuilds repairs to make customer_id nullable. It is applied by
// applyRepairCustomerOptional, which disables foreign keys around the table
// swap and verifies the result before committing; the SQL cannot run inside a
// normal transaction with foreign keys enabled.
const schemaV9 = `
CREATE TABLE repairs_new (
	id INTEGER PRIMARY KEY,
	customer_id INTEGER REFERENCES customers(id),
	device TEXT NOT NULL CHECK(length(trim(device)) BETWEEN 1 AND 300),
	issue TEXT NOT NULL CHECK(length(trim(issue)) BETWEEN 1 AND 5000),
	stage TEXT NOT NULL CHECK(stage IN ('new','in_progress','waiting_parts','lab_test','customer_test','repaired','no_repair')),
	notes TEXT NOT NULL DEFAULT '' CHECK(length(notes) <= 5000),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	pickup_at TEXT NOT NULL DEFAULT '',
	picked_up_by TEXT NOT NULL DEFAULT '',
	pickup_notes TEXT NOT NULL DEFAULT '',
	archived_at TEXT NOT NULL DEFAULT '',
	device_type TEXT NOT NULL DEFAULT '' CHECK(length(device_type) <= 300),
	brand TEXT NOT NULL DEFAULT '' CHECK(length(brand) <= 200),
	model TEXT NOT NULL DEFAULT '' CHECK(length(model) <= 200),
	serial TEXT NOT NULL DEFAULT '' CHECK(length(serial) <= 200)
) STRICT;

INSERT INTO repairs_new(id, customer_id, device, issue, stage, notes, created_at, updated_at,
	pickup_at, picked_up_by, pickup_notes, archived_at, device_type, brand, model, serial)
SELECT id, customer_id, device, issue, stage, notes, created_at, updated_at,
	pickup_at, picked_up_by, pickup_notes, archived_at, device_type, brand, model, serial
FROM repairs;

DROP TABLE repairs;
ALTER TABLE repairs_new RENAME TO repairs;
CREATE INDEX idx_repairs_customer ON repairs(customer_id);
CREATE INDEX idx_repairs_stage ON repairs(stage);
`

const schemaV7 = `
ALTER TABLE customers ADD COLUMN address TEXT NOT NULL DEFAULT '' CHECK(length(address) <= 300);
ALTER TABLE repairs ADD COLUMN device_type TEXT NOT NULL DEFAULT '' CHECK(length(device_type) <= 300);
ALTER TABLE repairs ADD COLUMN brand TEXT NOT NULL DEFAULT '' CHECK(length(brand) <= 200);
ALTER TABLE repairs ADD COLUMN model TEXT NOT NULL DEFAULT '' CHECK(length(model) <= 200);
ALTER TABLE repairs ADD COLUMN serial TEXT NOT NULL DEFAULT '' CHECK(length(serial) <= 200);
ALTER TABLE repairs ADD COLUMN legacy_id TEXT CHECK(legacy_id IS NULL OR length(legacy_id) <= 200);
UPDATE repairs SET device_type = device;
CREATE INDEX idx_repairs_legacy_id ON repairs(legacy_id);
`

const schemaV6 = `
CREATE TABLE users (
	id INTEGER PRIMARY KEY,
	username TEXT NOT NULL UNIQUE CHECK(length(username) BETWEEN 1 AND 100),
	password_hash TEXT NOT NULL,
	role TEXT NOT NULL CHECK(role IN ('admin','technician','viewer')),
	active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE auth_sessions (
	token_hash TEXT PRIMARY KEY CHECK(length(token_hash) = 64),
	csrf_token TEXT NOT NULL CHECK(length(csrf_token) BETWEEN 16 AND 128),
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	last_seen_at TEXT NOT NULL,
	revoked_at TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX idx_auth_sessions_user ON auth_sessions(user_id);
`

const schemaV5 = `
CREATE TABLE request_idempotency (
	key TEXT PRIMARY KEY CHECK(length(key) BETWEEN 8 AND 128),
	operation TEXT NOT NULL CHECK(length(operation) BETWEEN 1 AND 50),
	fingerprint TEXT NOT NULL CHECK(length(fingerprint) = 64),
	result_json TEXT NOT NULL,
	scope_id INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_request_idempotency_scope ON request_idempotency(scope_id);
`

const schemaV4 = `
CREATE TABLE attachments (
	id INTEGER PRIMARY KEY,
	repair_id INTEGER NOT NULL REFERENCES repairs(id) ON DELETE CASCADE,
	display_name TEXT NOT NULL CHECK(length(display_name) BETWEEN 1 AND 300),
	storage_name TEXT NOT NULL DEFAULT '' CHECK((state = 'purged' AND storage_name = '') OR (state != 'purged' AND length(storage_name) = 32)),
	media_type TEXT NOT NULL,
	size_bytes INTEGER NOT NULL CHECK(size_bytes > 0),
	sha256 TEXT NOT NULL CHECK(length(sha256) = 64),
	caption TEXT NOT NULL DEFAULT '' CHECK(length(caption) <= 1000),
	source TEXT NOT NULL DEFAULT 'cli' CHECK(length(source) BETWEEN 1 AND 50),
	state TEXT NOT NULL DEFAULT 'attached' CHECK(state IN ('attached','detached','purged')),
	detached_at TEXT NOT NULL DEFAULT '',
	purge_after TEXT NOT NULL DEFAULT '',
	created_by TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
) STRICT;

CREATE UNIQUE INDEX idx_attachments_storage ON attachments(storage_name) WHERE storage_name != '';
CREATE INDEX idx_attachments_repair ON attachments(repair_id);
CREATE INDEX idx_attachments_state ON attachments(state);

CREATE TABLE attachment_links (
	attachment_id INTEGER NOT NULL REFERENCES attachments(id) ON DELETE CASCADE,
	entry_id INTEGER NOT NULL REFERENCES repair_entries(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL,
	PRIMARY KEY (attachment_id, entry_id)
) STRICT;

CREATE INDEX idx_attachment_links_entry ON attachment_links(entry_id);
`

const schemaV3 = `
CREATE TABLE repair_parts (
	id INTEGER PRIMARY KEY,
	repair_id INTEGER NOT NULL REFERENCES repairs(id) ON DELETE CASCADE,
	description TEXT NOT NULL CHECK(length(trim(description)) BETWEEN 1 AND 300),
	quantity_milli INTEGER NOT NULL CHECK(quantity_milli > 0),
	unit TEXT NOT NULL DEFAULT 'pcs' CHECK(length(trim(unit)) BETWEEN 1 AND 20),
	unit_cost_cents INTEGER CHECK(unit_cost_cents IS NULL OR unit_cost_cents >= 0),
	unit_price_cents INTEGER NOT NULL CHECK(unit_price_cents >= 0),
	notes TEXT NOT NULL DEFAULT '' CHECK(length(notes) <= 1000),
	deleted_at TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_repair_parts_repair ON repair_parts(repair_id);

CREATE TABLE estimates (
	id INTEGER PRIMARY KEY,
	repair_id INTEGER NOT NULL REFERENCES repairs(id) ON DELETE CASCADE,
	version INTEGER NOT NULL,
	status TEXT NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','accepted','superseded')),
	note TEXT NOT NULL DEFAULT '' CHECK(length(note) <= 2000),
	subtotal_cents INTEGER NOT NULL CHECK(subtotal_cents >= 0),
	discount_kind TEXT NOT NULL DEFAULT 'none' CHECK(discount_kind IN ('none','amount','percent')),
	discount_value INTEGER NOT NULL DEFAULT 0 CHECK(discount_value >= 0),
	discount_cents INTEGER NOT NULL DEFAULT 0 CHECK(discount_cents >= 0),
	total_cents INTEGER NOT NULL CHECK(total_cents >= 0),
	currency TEXT NOT NULL DEFAULT 'EUR',
	created_by TEXT NOT NULL,
	created_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_estimates_repair ON estimates(repair_id);

CREATE TABLE estimate_lines (
	id INTEGER PRIMARY KEY,
	estimate_id INTEGER NOT NULL REFERENCES estimates(id) ON DELETE CASCADE,
	description TEXT NOT NULL CHECK(length(trim(description)) BETWEEN 1 AND 300),
	quantity_milli INTEGER NOT NULL CHECK(quantity_milli > 0),
	unit TEXT NOT NULL DEFAULT 'pcs',
	unit_price_cents INTEGER NOT NULL CHECK(unit_price_cents >= 0),
	line_total_cents INTEGER NOT NULL CHECK(line_total_cents >= 0),
	created_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_estimate_lines_estimate ON estimate_lines(estimate_id);
`

const schemaV2 = `
ALTER TABLE repairs ADD COLUMN pickup_at TEXT NOT NULL DEFAULT '';
ALTER TABLE repairs ADD COLUMN picked_up_by TEXT NOT NULL DEFAULT '';
ALTER TABLE repairs ADD COLUMN pickup_notes TEXT NOT NULL DEFAULT '';
ALTER TABLE repairs ADD COLUMN archived_at TEXT NOT NULL DEFAULT '';
ALTER TABLE customers ADD COLUMN archived_at TEXT NOT NULL DEFAULT '';
ALTER TABLE repair_entries ADD COLUMN deleted_at TEXT NOT NULL DEFAULT '';

CREATE TABLE repair_entry_revisions (
	id INTEGER PRIMARY KEY,
	entry_id INTEGER NOT NULL REFERENCES repair_entries(id) ON DELETE CASCADE,
	actor TEXT NOT NULL CHECK(length(actor) BETWEEN 1 AND 100),
	prev_kind TEXT NOT NULL,
	prev_body TEXT NOT NULL,
	prev_created_at TEXT NOT NULL,
	created_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_repair_entry_revisions_entry ON repair_entry_revisions(entry_id);
`

const schemaV1 = `
CREATE TABLE customers (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL CHECK(length(trim(name)) BETWEEN 1 AND 200),
	phone TEXT NOT NULL DEFAULT '' CHECK(length(phone) <= 100),
	email TEXT NOT NULL DEFAULT '' CHECK(length(email) <= 200),
	notes TEXT NOT NULL DEFAULT '' CHECK(length(notes) <= 5000),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE customer_history (
	id INTEGER PRIMARY KEY,
	customer_id INTEGER NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	actor TEXT NOT NULL CHECK(length(actor) BETWEEN 1 AND 100),
	action TEXT NOT NULL CHECK(length(action) BETWEEN 1 AND 50),
	detail TEXT NOT NULL,
	created_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_customer_history_customer ON customer_history(customer_id);

CREATE TABLE repairs (
	id INTEGER PRIMARY KEY,
	customer_id INTEGER NOT NULL REFERENCES customers(id),
	device TEXT NOT NULL CHECK(length(trim(device)) BETWEEN 1 AND 300),
	issue TEXT NOT NULL CHECK(length(trim(issue)) BETWEEN 1 AND 5000),
	stage TEXT NOT NULL CHECK(stage IN ('new','in_progress','waiting_parts','lab_test','customer_test','repaired','no_repair')),
	notes TEXT NOT NULL DEFAULT '' CHECK(length(notes) <= 5000),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_repairs_customer ON repairs(customer_id);
CREATE INDEX idx_repairs_stage ON repairs(stage);

CREATE TABLE repair_entries (
	id INTEGER PRIMARY KEY,
	repair_id INTEGER NOT NULL REFERENCES repairs(id) ON DELETE CASCADE,
	kind TEXT NOT NULL CHECK(kind IN ('work','test','contact','handover','note')),
	body TEXT NOT NULL CHECK(length(trim(body)) BETWEEN 1 AND 10000),
	created_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_repair_entries_repair ON repair_entries(repair_id);

CREATE TABLE repair_activity (
	id INTEGER PRIMARY KEY,
	repair_id INTEGER NOT NULL REFERENCES repairs(id) ON DELETE CASCADE,
	actor TEXT NOT NULL CHECK(length(actor) BETWEEN 1 AND 100),
	action TEXT NOT NULL CHECK(length(action) BETWEEN 1 AND 50),
	detail TEXT NOT NULL,
	created_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_repair_activity_repair ON repair_activity(repair_id);

CREATE TABLE seed_markers (
	key TEXT PRIMARY KEY,
	checksum TEXT NOT NULL,
	created_at TEXT NOT NULL
) STRICT;
`
