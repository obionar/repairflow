package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"repairflow/internal/errs"
)

// textResult pairs a machine payload with its human line so a command can
// render text without changing the documented JSON shape.
type textResult struct {
	data any
	text string
}

// ok renders a successful result.
func ok(w io.Writer, command string, asJSON bool, data any) int {
	if t, isText := data.(textResult); isText {
		if !asJSON {
			return writeText(w, t.text)
		}
		data = t.data
	}
	if asJSON {
		return writeJSON(w, envelope{OK: true, Command: command, Contract: contractVersion, Data: data})
	}
	switch v := data.(type) {
	case initDemoResult:
		text, _ := renderData(&v.Init)
		out := text
		if v.Seed != nil {
			seedText, _ := renderData(*v.Seed)
			out += seedText
		}
		return writeText(w, out)
	}
	if text, handled := renderData(data); handled {
		return writeText(w, text)
	}
	// Unknown type in text mode: fall back to JSON so nothing renders empty.
	return writeJSON(w, envelope{OK: true, Command: command, Contract: contractVersion, Data: data})
}

// fail renders a classified failure and returns the contract exit code.
func fail(w io.Writer, command string, asJSON bool, err error, debug bool) int {
	code := errs.CodeOf(err)
	if asJSON {
		if writeErr := writeJSON(w, envelope{
			OK: false, Command: command, Contract: contractVersion,
			Error: &errorBody{Code: string(code), Message: err.Error()},
		}); writeErr != 0 {
			return 1
		}
	} else if _, writeErr := fmt.Fprintf(w, "error (%s): %s\n", code, err.Error()); writeErr != nil {
		return 1
	}
	if debug && code == errs.CodeInternal {
		if _, writeErr := fmt.Fprint(w, stackDebug()); writeErr != nil {
			return 1
		}
	}
	return errs.ExitCode(err)
}

type envelope struct {
	OK       bool       `json:"ok"`
	Command  string     `json:"command"`
	Contract int        `json:"contract"`
	Data     any        `json:"data,omitempty"`
	Error    *errorBody `json:"error,omitempty"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeText(w io.Writer, text string) int {
	if _, err := fmt.Fprint(w, text); err != nil {
		return 1
	}
	return 0
}

func writeJSON(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return 1
	}
	return 0
}

// Focused per-command help. Each line is the same usage string the command
// reports when it is misused, so there is one flag matrix (docs/CLI-CONTRACT.md
// is the other). Unknown commands fall back to the global page.
const (
	usageUser        = "repairflow user <add|list|passwd|enable|disable> ..."
	usageUserAdd     = "repairflow user add USERNAME --role admin|technician|viewer [--password-stdin]"
	usageUserList    = "repairflow user list"
	usageUserPasswd  = "repairflow user passwd USERNAME [--password-stdin]"
	usageUserEnable  = "repairflow user enable USERNAME"
	usageUserDisable = "repairflow user disable USERNAME"
	usageServe       = "repairflow serve"
	usageService     = "repairflow service <install|status|remove> ..."
)

var commandUsage = map[string]string{
	"init":            "repairflow init [--demo [--demo-user USERNAME]] [--language CODE] [--backup ARCHIVE] | init --interactive",
	"config check":    "repairflow config check",
	"status":          "repairflow status",
	"doctor":          "repairflow doctor",
	"cleanup":         "repairflow cleanup",
	"serve":           usageServe,
	"customer":        "repairflow customer <add|list|show|history|update|archive|unarchive> ...",
	"repair":          "repairflow repair <add|list|show|update|stage|entry|pickup|label|archive|part-*> ...",
	"printer":         "repairflow printer <show|set|test> ...",
	"estimate":        "repairflow estimate <create|list|show|accept> ...",
	"attachment":      "repairflow attachment <add|list|show|export|caption|link|unlink|detach|restore|purge> ...",
	"user":            usageUser,
	"user add":        usageUserAdd,
	"user list":       usageUserList,
	"user passwd":     usageUserPasswd,
	"user enable":     usageUserEnable,
	"user disable":    usageUserDisable,
	"backup":          "repairflow backup <create|verify> ...",
	"restore":         "repairflow restore ARCHIVE --home PATH",
	"seed":            "repairflow seed demo [--demo-user USERNAME]",
	"demo":            "repairflow demo reset --confirm",
	"service":         usageService,
	"service install": "repairflow service install [--init systemd|openrc|none] [--name NAME] [--no-enable]",
	"service status":  "repairflow service status [--init systemd|openrc|none] [--name NAME]",
	"service remove":  "repairflow service remove [--init systemd|openrc|none] [--name NAME]",
	"update":          "repairflow update <check|apply|verify|keygen|sign> ...",
	"version":         "repairflow version",
	"help":            "repairflow help",
}

// focusedUsage renders the short help block for the requested command (and
// subcommand) and reports whether it is known.
func focusedUsage(args []string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	key := args[0]
	if len(args) > 1 {
		if _, ok := commandUsage[key+" "+args[1]]; ok {
			key += " " + args[1]
		}
	}
	line, ok := commandUsage[key]
	if !ok {
		return "", false
	}
	return "Usage:\n  " + line +
		"\n\nGlobal flags: --home PATH, --json, --debug, --version, --help/-h.\n" +
		"Run \"repairflow help\" for the full command list.\n", true
}

const usageText = `repairflow - local-first repair-shop workspace (single Go binary)

Usage:
  repairflow [--home PATH] [--json] [--debug] <command> [arguments]

Workspace:
  init [--demo [--demo-user USERNAME]] [--language CODE] [--backup ARCHIVE]
                     Initialize, or migrate an outdated schema after verifying
                     the absolute pre-migration backup ARCHIVE. --demo
                     seeds a full synthetic shop (customers, repairs in every
                     stage, parts, estimates, files); --demo-user also creates that demo
                     admin using REPAIRFLOW_NEW_PASSWORD
  init --interactive  Prompt for workspace, account, port, language and demo data
  config check       Validate the workspace configuration
  status             Workspace summary (no business-record edits)
  doctor             Health diagnostics: workspace, schema, integrity, records,
                     demo marker and attachment bytes (may resolve pending deletes)
  cleanup            Remove stale staging files and cache (never user data)
  serve              Run the embedded web interface (config web.host/web.port)

Customers:
  customer add --name NAME [--phone P] [--email E] [--address A] [--notes N]
               [--idempotency-key K]
  customer list [--search TEXT] [--page N] [--per-page N] [--include-archived]
  customer show ID | customer history ID
  customer update ID [--name V] [--phone V] [--email V] [--address V] [--notes V]
                     [--revision R]
  customer archive ID | customer unarchive ID

Repairs (lifecycle: new, in_progress, waiting_parts, lab_test, customer_test,
repaired, no_repair; terminal stages need --reopen to return to in_progress):
  (ticket remains a documented one-release alias of repair)
  repair add [--customer ID] [--device D | --device-type T] [--brand B]
             [--model M] [--serial S] --issue I [--notes N] [--idempotency-key K]
  repair list [--search TEXT] [--stage S] [--customer ID] [--page N]
              [--per-page N] [--include-archived]
  repair show ID [--include-deleted]
  repair update ID [--customer ID | --no-customer] [--device V] [--device-type V]
                   [--brand V] [--model V] [--serial V] [--issue V] [--notes V] [--revision R]
  repair stage ID STAGE [--reopen]
  repair entry ID --kind work|test|contact|handover|note --body TEXT
  repair entry-edit ENTRY_ID [--kind K] [--body B]
  repair entry-delete ENTRY_ID | repair entry-restore ENTRY_ID
  repair pickup ID --at RFC3339|YYYY-MM-DD --by NAME [--notes N]
  repair label ID                       # print the intake sticker
  repair archive ID | repair unarchive ID

Label printer (Marklife P15 through the local p15 daemon):
  printer show | printer set p15|none | printer test

Parts and estimates (integer cents, milli-quantities, immutable estimates):
  repair part-add TICKET_ID --description D --qty Q [--unit U] [--cost C]
                  --price P [--notes N]
  repair part-list TICKET_ID [--include-deleted]
  repair part-update PART_ID [--description D] [--qty Q] [--unit U] [--cost C]
                   [--clear-cost] [--price P] [--notes N] [--revision R]
  repair part-remove PART_ID | repair part-restore PART_ID
  estimate create TICKET_ID [--note N] [--discount-amount C | --discount-percent P]
  estimate list TICKET_ID | estimate show ID | estimate accept ID

Attachments (original bytes, hash-verified; detach/restore/purge with retention):
  attachment add --repair ID --file PATH [--caption C] [--entry E] [--source S]
  attachment list --repair ID [--all] | attachment show ID
  attachment export ID --out PATH
  attachment caption ID --caption TEXT
  attachment link ID --entry E | attachment unlink ID --entry E
  attachment detach ID | attachment restore ID | attachment purge ID --confirm

Accounts (password via --password-stdin, REPAIRFLOW_NEW_PASSWORD or an
interactive prompt; 6-256 characters, never a command-line argument):
  user add USERNAME --role admin|technician|viewer [--password-stdin]
  user list | user passwd USERNAME [--password-stdin]
  user enable USERNAME | user disable USERNAME

Backup and restore (verified .rfbak archives):
  backup create --output ABSOLUTE_PATH   # consistent DB snapshot + verified originals
  backup verify ARCHIVE
  restore ARCHIVE --home PATH            # targets an empty/new workspace only

Service and offline updates:
  service install [--init systemd|openrc|none] [--name NAME] [--no-enable]
  service status | service remove        # systemd user unit or /etc/init.d script
                                         # --name selects a non-default instance
  update check --manifest M --signature S
  update apply --file ARTIFACT --manifest M --signature S --confirm
  update verify --file FILE --signature SIG
  update keygen --out DIR | update sign --key K --manifest M --out SIG

Demo:
  seed demo [--demo-user USERNAME]
                     Seed the full synthetic shop (refuses over real records);
                     --demo-user creates that demo admin, password from
                     REPAIRFLOW_NEW_PASSWORD
  demo reset --confirm  Reset a *proven* demo workspace to pristine state

Other:
  version            Print build and version information
  help               Show this help

Global flags:
  --home PATH        Workspace directory (default: $REPAIRFLOW_HOME, then ~/.repairflow)
  --json             Machine-readable JSON output
  --debug            Structured diagnostics on stderr (also REPAIRFLOW_DEBUG=1)
  --version          Print version information
  --help, -h         Show this help

Mutating commands accept --actor NAME for audit attribution (default: $USER).
Create commands accept --idempotency-key K (8-128 chars): same key + same payload
replays the original result; same key + different payload is a conflict (exit 3).

Exit codes:
  0 success   1 internal error   2 usage/validation/permission
  3 state conflict (e.g. already/not initialized, stale revision)   4 not found

Use --debug or REPAIRFLOW_DEBUG=1 for structured diagnostics and internal stack traces.
Machine-readable errors use stable codes; see docs/CLI-CONTRACT.md.
`
