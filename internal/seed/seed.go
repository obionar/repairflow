// Package seed provides the isolated synthetic demo dataset. Every record is
// obviously fake ("DEMO" names and devices), created through the same app
// services as real data, and marked in seed_markers. Seeding is refusal-first:
// it never runs over existing business data and never touches anything outside
// the given workspace database.
package seed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

// DemoKey identifies the demo dataset version in seed_markers; DemoStateKey
// stores the content fingerprint of the pristine seeded state (provenance
// for the demo reset guard).
//
// demo-v2 added part lines, estimates (accepted + draft), pickup state and
// extra repairs/media. demo-v3 is the "big pack": 16 customers and 44 repairs
// generated across every stage, including archived and picked-up records, so a
// newcomer can explore a populated shop instead of an empty one. Older demo
// datasets are not reseeded or reset (synthetic data; recreate the workspace).
const (
	DemoKey      = "demo-v3"
	DemoStateKey = "demo-v3-state"
)

// DemoActor is the audit identity recorded for seeded rows.
const DemoActor = "demo-seed"

// Result reports what the demo seed did. User is only set when the caller
// asked for a demo operator account (see Options).
type Result struct {
	Seeded        bool        `json:"seeded"`
	AlreadySeeded bool        `json:"already_seeded"`
	Customers     int         `json:"customers"`
	Repairs       int         `json:"repairs"`
	Entries       int         `json:"entries"`
	Parts         int         `json:"parts"`
	Estimates     int         `json:"estimates"`
	Attachments   int         `json:"attachments"`
	User          *UserResult `json:"user,omitempty"`
}

// Options configures optional provisioning beyond the dataset itself. The
// password is never stored in the result, logged or echoed.
type Options struct {
	User     string
	Password string
}

// Counts of the deterministic dataset (used by Reset provenance checks and by
// the seeding report). They are derived from the dataset itself so adding rows
// cannot leave the reset guard or the tests out of date. DemoParts counts
// created rows, including the lines that end up soft-deleted.
var (
	DemoCustomers   = len(demoData.customers)
	DemoRepairs     = len(demoData.repairs)
	DemoEntries     = countDemo(func(r demoRepair) int { return len(r.entries) })
	DemoParts       = countDemo(func(r demoRepair) int { return len(r.parts) })
	DemoAttachments = len(demoMedia)
	DemoEstimates   = countDemo(func(r demoRepair) int {
		if r.estimate != nil {
			return 1
		}
		return 0
	})
)

func countDemo(f func(demoRepair) int) int {
	n := 0
	for _, r := range demoData.repairs {
		n += f(r)
	}
	return n
}

type demoCustomer struct{ name, phone, email, notes string }

type demoEntry struct{ kind, body string }

type demoStageStep struct {
	stage  string
	reopen bool
}

// demoPart is one used-part line on a demo repair. CostCents < 0 means
// "unknown cost" (stored as NULL). Removed marks a soft-deleted line so the
// demo shows the recovery fold instead of only pristine rows.
type demoPart struct {
	description string
	qtyMilli    int64
	unit        string
	costCents   int64
	priceCents  int64
	notes       string
	removed     bool
}

// demoEstimate creates a versioned estimate snapshot after the repair's
// active parts exist; Accept records the customer's approval.
type demoEstimate struct {
	note          string
	discountKind  string
	discountValue int64
	accept        bool
}

type demoPickup struct{ at, by, notes string }

type demoRepair struct {
	customer   int
	archived   bool
	device     string
	issue      string
	notes      string
	stageChain []demoStageStep
	entries    []demoEntry
	parts      []demoPart
	estimate   *demoEstimate
	pickup     *demoPickup
}

// demoData is the deterministic synthetic dataset. Timestamps are runtime;
// content (and therefore the manifest checksum) is fixed. The dataset covers
// every workflow the UI can show: new/intake, diagnostics, waiting for parts,
// repair, unsuccessful repair, customer test, repair complete with pickup,
// parts with unknown cost, a removed part, an accepted estimate with a
// discount and a draft estimate.
var showcaseData = struct {
	customers []demoCustomer
	repairs   []demoRepair
}{
	customers: []demoCustomer{
		{"Ada Beispiel (DEMO)", "+49 000 0001", "ada@example.invalid", "Synthetic demo customer. Prefers email contact."},
		{"Boris Demo (DEMO)", "+49 000 0002", "boris@example.invalid", "Synthetic demo customer."},
		{"Chloé Test (DEMO)", "+33 000 0003", "chloe@example.invalid", "Synthetic demo customer. Drop-off on Fridays."},
		{"Derya Muster (DEMO)", "+49 000 0004", "derya@example.invalid", "Synthetic demo customer. Café owner; invoices by email."},
	},
	repairs: []demoRepair{
		{
			customer: 0, device: "ThinkPad T480 (DEMO)", issue: "Does not power on; burning smell near the charging port.",
			notes: "Synthetic demo repair. Intake photos are intentionally not part of this dataset.",
			stageChain: []demoStageStep{
				{"in_progress", false}, {"waiting_parts", false}, {"in_progress", false}, {"repaired", false},
			},
			entries: []demoEntry{
				{"work", "Replaced burnt DC-in jack and tested charging circuit."},
				{"test", "24h burn-in passed; battery charges to 100%."},
				{"handover", "Device collected by customer; receipt handed over."},
			},
			parts: []demoPart{
				{description: "DC-in power jack", qtyMilli: 1000, unit: "pcs", costCents: 1450, priceCents: 3900, notes: "OEM-compatible replacement."},
				{description: "Thermal paste", qtyMilli: 500, unit: "g", costCents: 320, priceCents: 900},
			},
			estimate: &demoEstimate{note: "Goodwill discount after the parts delay.", discountKind: "percent", discountValue: 10, accept: true},
			pickup:   &demoPickup{at: "2026-11-09T14:20:00Z", by: "Ada Beispiel", notes: "Collected with receipt; paid on collection."},
		},
		{
			customer: 1, device: "iPhone 12 (DEMO)", issue: "Cracked screen; touch input dead on the lower half.",
			stageChain: []demoStageStep{{"in_progress", false}},
			entries: []demoEntry{
				{"work", "Screen assembly removed; replacement part on order."},
			},
			parts: []demoPart{
				{description: "Screen assembly (OLED)", qtyMilli: 1000, unit: "pcs", costCents: 8900, priceCents: 14900},
			},
			estimate: &demoEstimate{note: "Quote for screen assembly including labour; awaiting customer approval."},
		},
		{
			customer: 1, device: "Bosch dishwasher (DEMO)", issue: "Error E15; water collected in the base pan.",
			notes:      "Synthetic demo repair.",
			stageChain: []demoStageStep{{"waiting_parts", false}},
			entries: []demoEntry{
				{"note", "Dried base pan and ran diagnostics; leak sensor triggers intermittently."},
				{"contact", "Called customer to approve the sump-hose replacement cost."},
			},
			parts: []demoPart{
				{description: "Sump hose", qtyMilli: 2000, unit: "pcs", costCents: 640, priceCents: 1490},
			},
		},
		{
			customer: 2, device: "Gaming PC (DEMO)", issue: "Random reboots under GPU load.",
			stageChain: []demoStageStep{{"in_progress", false}, {"lab_test", false}},
			entries: []demoEntry{
				{"test", "FurMark 2h: 3 reboots at 82C hotspot; PSU rails within spec."},
			},
			parts: []demoPart{
				{description: "650 W PSU (80+ Gold)", qtyMilli: 1000, unit: "pcs", costCents: 5200, priceCents: 8900, notes: "Cost price not yet confirmed by supplier."},
			},
		},
		{
			customer: 2, device: "Kindle Paperwhite (DEMO)", issue: "Screen frozen on boot logo after firmware update.",
			stageChain: []demoStageStep{{"no_repair", false}},
			entries: []demoEntry{
				{"handover", "Board-level NAND failure; device returned unrepaired, no charge."},
			},
		},
		{
			customer: 3, device: "Rancilio Silvia espresso machine (DEMO)", issue: "No pressure at the group head; pump runs constantly.",
			stageChain: []demoStageStep{{"in_progress", false}, {"customer_test", false}},
			entries: []demoEntry{
				{"work", "Replaced the solenoid valve and descaled the brew circuit."},
				{"test", "9 bar at the group head after 3 min warm-up; ready for the customer's trial."},
			},
			parts: []demoPart{
				{description: "Solenoid valve", qtyMilli: 1000, unit: "pcs", costCents: 2100, priceCents: 4200},
			},
		},
		{
			customer: 3, device: "Sony WH-1000XM4 (DEMO)", issue: "Right cup silent after water damage.",
			stageChain: []demoStageStep{{"in_progress", false}},
			entries: []demoEntry{
				{"work", "Cleaned the board and reflowed the right-channel output; headband replacement cancelled."},
			},
			parts: []demoPart{
				{description: "Headband assembly", qtyMilli: 1000, unit: "pcs", costCents: 3000, priceCents: 6500, removed: true},
				{description: "Foam ear pads", qtyMilli: 1000, unit: "pair", costCents: 500, priceCents: 1500},
			},
		},
		{
			customer: 0, device: "MacBook Air M1 (DEMO)", issue: "Keyboard repeats keys after a liquid spill.",
			notes:      "Dropped off this morning; diagnosis not started yet.",
			stageChain: nil,
		},
	},
}

// demoData is the full dataset: the hand-written showcase rows above plus the
// generated bulk pack. Everything is deterministic, so two demo workspaces
// anywhere hold the same content and the manifest checksum is stable.
var demoData = struct {
	customers []demoCustomer
	repairs   []demoRepair
}{
	customers: append(append([]demoCustomer{}, showcaseData.customers...), bulkCustomers()...),
	repairs:   append(append([]demoRepair{}, showcaseData.repairs...), bulkRepairs(len(showcaseData.customers))...),
}

// bulkCustomers are the additional clearly-fake customers. The mix of scripts
// (Greek, Cyrillic, Turkish, Latin with diacritics) exercises sorting, search
// and rendering with non-ASCII names.
func bulkCustomers() []demoCustomer {
	return []demoCustomer{
		{"Ελένη Παπαδοπούλου (DEMO)", "+357 00 200001", "eleni@example.invalid", "Synthetic demo customer. Calls in the morning."},
		{"Γιώργος Δημητρίου (DEMO)", "+357 00 200002", "giorgos@example.invalid", "Synthetic demo customer. Regular, prefers SMS."},
		{"Марia Иванова (DEMO)", "+7 000 200003", "maria@example.invalid", "Synthetic demo customer. Invoices in Russian."},
		{"Дмитрий Соколов (DEMO)", "+7 000 200004", "dmitry@example.invalid", "Synthetic demo customer."},
		{"Elif Yılmaz (DEMO)", "+90 000 200005", "elif@example.invalid", "Synthetic demo customer. Prefers WhatsApp."},
		{"Mehmet Kaya (DEMO)", "+90 000 200006", "mehmet@example.invalid", "Synthetic demo customer. Brings devices in pairs."},
		{"Sophie Martin (DEMO)", "+33 000 200007", "sophie@example.invalid", "Synthetic demo customer."},
		{"Lukas Weber (DEMO)", "+49 000 200008", "lukas@example.invalid", "Synthetic demo customer. B2B account, pays on invoice."},
		{"Carmen Ruiz (DEMO)", "+34 000 200009", "carmen@example.invalid", "Synthetic demo customer."},
		{"Nikos Andreou (DEMO)", "+357 00 200010", "nikos@example.invalid", "Synthetic demo customer. Walk-in, cash only."},
		{"Anna Kowalska (DEMO)", "+48 000 200011", "anna@example.invalid", "Synthetic demo customer."},
		{"James Okafor (DEMO)", "+44 000 200012", "james@example.invalid", "Synthetic demo customer. Ships devices by courier."},
	}
}

// bulkRepairs generates the rest of the pack. The spread is deliberate: every
// stage appears several times, entries cover all five kinds, and some records
// carry a part with unknown cost, a soft-deleted line, a draft or accepted
// estimate, a pickup, and an archived state.
func bulkRepairs(customerOffset int) []demoRepair {
	devices := []string{
		"Dell Latitude 5420", "HP EliteBook 840", "Lenovo IdeaPad 5", "MacBook Pro 13 (2019)",
		"Samsung Galaxy S21", "Google Pixel 6", "Xiaomi Redmi Note 12", "iPad Air (2022)",
		"PlayStation 5", "Xbox Series S", "Nintendo Switch OLED", "LG 27\" monitor",
		"Canon MG5750 printer", "Brother HL-L2350", "Miele washing machine", "Bosch fridge",
		"Philips air fryer", "Dyson V11 vacuum",
	}
	issues := []string{
		"Does not power on.", "Battery drains in under an hour.", "Overheating under load.",
		"Liquid damage; unstable behaviour.", "Screen flickers at low brightness.",
		"No sound from the speakers.", "Charging port loose.", "Storage full; slow boot.",
		"Wi-Fi drops every few minutes.", "Boot loop after an update.", "Loud fan noise.",
		"Dents and a cracked bezel.", "Paper jam every print.", "Drum error on start-up.",
		"Water leak during the spin cycle.", "Compressor runs but does not cool.",
		"Display shows an error code.", "Battery does not charge at all.",
	}
	entryText := []string{
		"Diagnosed the fault and ordered parts", "Cleaned the board and reseated connectors",
		"Ran a burn-in test", "Explained the options and quoted the repair",
		"Replaced the faulty component", "Updated the firmware and reset settings",
		"Confirmed the fault is intermittent", "Checked the power rails",
		"Tested the device with the customer present", "Prepared the device for collection",
		"Recorded serial numbers and accessories", "Verified the repair after reassembly",
	}
	stagePool := []struct {
		stage string
		chain []demoStageStep
	}{
		{"new", nil},
		{"in_progress", []demoStageStep{{"in_progress", false}}},
		{"waiting_parts", []demoStageStep{{"in_progress", false}, {"waiting_parts", false}}},
		{"lab_test", []demoStageStep{{"in_progress", false}, {"lab_test", false}}},
		{"customer_test", []demoStageStep{{"in_progress", false}, {"customer_test", false}}},
		{"repaired", []demoStageStep{{"in_progress", false}, {"waiting_parts", false}, {"in_progress", false}, {"repaired", false}}},
		{"no_repair", []demoStageStep{{"no_repair", false}}},
	}
	kinds := []string{"work", "test", "contact", "handover", "note"}
	partPool := []demoPart{
		{description: "Battery pack", qtyMilli: 1000, unit: "pcs", costCents: 2400, priceCents: 4900},
		{description: "Charging board", qtyMilli: 1000, unit: "pcs", costCents: 1800, priceCents: 3900},
		{description: "Display cable", qtyMilli: 1000, unit: "pcs", costCents: 900, priceCents: 2200},
		{description: "Thermal pads", qtyMilli: 2000, unit: "pcs", costCents: 250, priceCents: 800},
		{description: "Cooling fan", qtyMilli: 1000, unit: "pcs", costCents: 1200, priceCents: 2900},
		{description: "USB-C port", qtyMilli: 1000, unit: "pcs", costCents: 700, priceCents: 1900},
		{description: "Silicone grease", qtyMilli: 300, unit: "g", costCents: 180, priceCents: 600},
		{description: "Rubber feet set", qtyMilli: 1000, unit: "set", costCents: 120, priceCents: 450},
	}
	out := make([]demoRepair, 0, 36)
	for i := 0; i < 36; i++ {
		s := stagePool[i%len(stagePool)]
		r := demoRepair{
			customer:   customerOffset + i%12,
			device:     fmt.Sprintf("%s (DEMO %02d)", devices[i%len(devices)], i+1),
			issue:      issues[(i*5)%len(issues)],
			notes:      "Synthetic demo repair from the bulk pack.",
			stageChain: s.chain,
			archived:   i%9 == 0,
		}
		for j := 0; j < 1+i%3; j++ {
			r.entries = append(r.entries, demoEntry{
				kind: kinds[(i+j)%len(kinds)],
				body: fmt.Sprintf("%s (%s step %d).", entryText[(i*3+j)%len(entryText)], s.stage, j+1),
			})
		}
		if i%3 == 0 {
			r.parts = append(r.parts, partPool[i%len(partPool)])
		}
		if i%5 == 0 {
			// Every fifth repair shows a line whose cost is still unknown.
			r.parts = append(r.parts, demoPart{
				description: "Consumable kit", qtyMilli: 1000, unit: "pcs",
				costCents: -1, priceCents: 950, notes: "Supplier price pending.",
			})
		}
		if i%11 == 0 && len(r.parts) > 1 {
			r.parts[0].removed = true
		}
		if i%4 == 0 {
			// An estimate needs at least one active part line to price.
			if len(r.parts) == 0 {
				r.parts = append(r.parts, partPool[(i+3)%len(partPool)])
			}
			r.estimate = &demoEstimate{
				note:          "Bulk demo estimate covering parts and labour.",
				discountKind:  "percent",
				discountValue: 5,
				accept:        i%8 == 0,
			}
		}
		if s.stage == "repaired" && i%2 == 0 {
			r.pickup = &demoPickup{
				at:    "2026-11-05T10:00:00Z",
				by:    "Demo Customer",
				notes: "Collected after a successful test.",
			}
		}
		out = append(out, r)
	}
	return out
}

// Demo seeds the synthetic dataset without any operator account.
func Demo(ctx context.Context, svc *app.Service) (Result, error) {
	return DemoWithOptions(ctx, svc, Options{})
}

// DemoWithOptions seeds the synthetic dataset and, when opts.User is set,
// provisions one demo operator account (see ensureDemoUser). The dataset seed
// is idempotent: an existing demo workspace is left untouched, but a missing
// demo account is still created on request.
func DemoWithOptions(ctx context.Context, svc *app.Service, opts Options) (Result, error) {
	res, err := seedDataset(ctx, svc)
	if err != nil {
		return Result{}, err
	}
	user, err := ensureDemoUser(ctx, svc, opts)
	if err != nil {
		return Result{}, err
	}
	res.User = user
	return res, nil
}

// seedDataset seeds the synthetic dataset atomically. It refuses to run when
// any business record already exists (the demo must never mix with real data)
// and is idempotent once the seed marker is present.
func seedDataset(ctx context.Context, svc *app.Service) (Result, error) {
	if found, _, err := svc.SeedMarker(ctx, DemoKey); err != nil {
		return Result{}, err
	} else if found {
		return Result{AlreadySeeded: true}, nil
	}
	customers, repairs, err := svc.Counts(ctx)
	if err != nil {
		return Result{}, err
	}
	if customers > 0 || repairs > 0 {
		return Result{}, errs.New(errs.CodeConflict,
			"refusing to seed demo data: workspace already holds %d customer(s) and %d repair(s); demo data must stay isolated",
			customers, repairs)
	}

	res := Result{Seeded: true}
	entryIDs := map[[2]int]int64{} // {repair,entry} -> created entry id
	repairIDs := make([]int64, len(demoData.repairs))
	err = svc.InTx(ctx, func(ctx context.Context) error {
		ids := make([]int64, len(demoData.customers))
		for i, c := range demoData.customers {
			created, err := svc.CreateCustomer(ctx, DemoActor, app.CustomerInput{
				Name: c.name, Phone: c.phone, Email: c.email, Notes: c.notes,
			})
			if err != nil {
				return err
			}
			ids[i] = created.ID
			res.Customers++
		}
		for ti, t := range demoData.repairs {
			created, err := svc.CreateRepair(ctx, DemoActor, app.RepairInput{
				CustomerID: ids[t.customer], Device: t.device, Issue: t.issue, Notes: t.notes,
			})
			if err != nil {
				return err
			}
			res.Repairs++
			repairIDs[ti] = created.ID
			for _, step := range t.stageChain {
				if _, err := svc.SetRepairStage(ctx, DemoActor, created.ID, step.stage, step.reopen); err != nil {
					return err
				}
			}
			for ei, e := range t.entries {
				entry, err := svc.AddRepairEntry(ctx, DemoActor, created.ID, e.kind, e.body)
				if err != nil {
					return err
				}
				entryIDs[[2]int{ti, ei}] = entry.ID
				res.Entries++
			}
			for _, p := range t.parts {
				part, err := svc.AddRepairPart(ctx, DemoActor, created.ID, app.PartInput{
					Description:    p.description,
					QuantityMilli:  p.qtyMilli,
					Unit:           p.unit,
					UnitCostCents:  optionalCents(p.costCents),
					UnitPriceCents: p.priceCents,
					Notes:          p.notes,
				})
				if err != nil {
					return err
				}
				res.Parts++
				if !p.removed {
					continue
				}
				if _, err := svc.DeleteRepairPart(ctx, DemoActor, part.ID); err != nil {
					return err
				}
			}
			if t.estimate != nil {
				est, err := svc.CreateEstimate(ctx, DemoActor, created.ID, app.EstimateInput{
					Note:          t.estimate.note,
					DiscountKind:  t.estimate.discountKind,
					DiscountValue: t.estimate.discountValue,
				})
				if err != nil {
					return err
				}
				res.Estimates++
				if t.estimate.accept {
					if _, err := svc.AcceptEstimate(ctx, DemoActor, est.ID); err != nil {
						return err
					}
				}
			}
			if t.pickup != nil {
				if _, err := svc.SetRepairPickup(ctx, DemoActor, created.ID,
					t.pickup.at, t.pickup.by, t.pickup.notes); err != nil {
					return err
				}
			}
			if t.archived {
				if _, err := svc.ArchiveRepair(ctx, DemoActor, created.ID, true); err != nil {
					return err
				}
			}
		}
		// Media: intake files plus photos on log entries, all
		// generated in code and clearly synthetic. Bytes go through the same
		// attachment service as real uploads (hashes, audit, links).
		if err := attachDemoMedia(ctx, svc, repairIDs, entryIDs); err != nil {
			return err
		}
		res.Attachments = len(demoMedia)
		fp, err := svc.BusinessFingerprint(ctx)
		if err != nil {
			return err
		}
		if err := svc.PutSeedMarker(ctx, DemoStateKey, fp); err != nil {
			return err
		}
		return svc.PutSeedMarker(ctx, DemoKey, manifestChecksum())
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

// optionalCents maps the demo convention (negative = unknown cost) to the
// app's nil-means-unknown representation.
func optionalCents(v int64) *int64 {
	if v < 0 {
		return nil
	}
	return &v
}

// attachDemoMedia uploads the generated sample files via the shared
// attachment service. Needs the file store bound on svc.
func attachDemoMedia(ctx context.Context, svc *app.Service, repairIDs []int64, entryIDs map[[2]int]int64) error {
	for _, m := range demoMedia {
		repairID := repairIDs[m.repair]
		entryID := int64(0)
		if m.entry >= 0 {
			entryID = entryIDs[[2]int{m.repair, m.entry}]
		}
		if _, err := svc.AddAttachment(ctx, DemoActor, repairID, entryID,
			m.fileName, bytes.NewReader(m.mediaPayload()), m.caption, m.source); err != nil {
			return err
		}
	}
	return nil
}

// manifestChecksum fingerprints the demo content so an operator can verify
// which dataset version a workspace carries. Timestamps are excluded.
func manifestChecksum() string {
	h := sha256.New()
	h.Write([]byte(DemoKey))
	for _, c := range demoData.customers {
		h.Write([]byte{0x00})
		h.Write([]byte(c.name + "\x1f" + c.phone + "\x1f" + c.email + "\x1f" + c.notes))
	}
	for _, t := range demoData.repairs {
		h.Write([]byte{0x00})
		h.Write([]byte(t.device + "\x1f" + t.issue + "\x1f" + t.notes))
		for _, s := range t.stageChain {
			h.Write([]byte{0x01})
			h.Write([]byte(s.stage))
		}
		for _, e := range t.entries {
			h.Write([]byte{0x02})
			h.Write([]byte(e.kind + "\x1f" + e.body))
		}
		for _, p := range t.parts {
			h.Write([]byte{0x04})
			h.Write([]byte(p.description + "\x1f" + p.unit + "\x1f" + p.notes + "\x1f"))
			h.Write([]byte(strconv.FormatInt(p.qtyMilli, 10) + "\x1f" + strconv.FormatInt(p.costCents, 10) + "\x1f" +
				strconv.FormatInt(p.priceCents, 10) + "\x1f" + map[bool]string{true: "removed", false: "active"}[p.removed]))
		}
		if t.estimate != nil {
			h.Write([]byte{0x05})
			h.Write([]byte(t.estimate.note + "\x1f" + t.estimate.discountKind + "\x1f" +
				strconv.FormatInt(t.estimate.discountValue, 10) + "\x1f" + map[bool]string{true: "accepted", false: "draft"}[t.estimate.accept]))
		}
		if t.pickup != nil {
			h.Write([]byte{0x06})
			h.Write([]byte(t.pickup.at + "\x1f" + t.pickup.by + "\x1f" + t.pickup.notes))
		}
		if t.archived {
			h.Write([]byte{0x07})
		}
	}
	for _, m := range demoMedia {
		h.Write([]byte{0x03})
		sum := sha256.Sum256(m.mediaPayload())
		h.Write([]byte(m.fileName + "\x1f" + m.caption + "\x1f"))
		h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}
