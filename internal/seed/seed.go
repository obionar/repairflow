// Package seed provides the isolated synthetic demo dataset. Every record is
// obviously fake (funny fictional names and devices), created through the same app
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
// demo-v5 is the simplified funny-names pack: 16 customers and 44 repairs
// generated across every stage, including archived and picked-up records, so a
// newcomer can explore a populated shop instead of an empty one. It has
// whole-piece parts and no estimates. Older demo datasets are not reseeded or
// reset (synthetic data; recreate the workspace).
const (
	DemoKey      = "demo-v5"
	DemoStateKey = "demo-v5-state"
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
	DemoEstimates   = 0 // the web demo no longer creates estimates
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
	pickup     *demoPickup
}

// demoData is the deterministic synthetic dataset. Timestamps are runtime;
// content (and therefore the manifest checksum) is fixed. The dataset covers
// every workflow the UI can show: new/intake, diagnostics, waiting for parts,
// repair, unsuccessful repair, customer test, repair complete with pickup,
// parts with unknown cost and a removed part.
var showcaseData = struct {
	customers []demoCustomer
	repairs   []demoRepair
}{
	customers: []demoCustomer{
		{"Sir Reginald Solderworth", "+49 000 0001", "reginald@example.invalid", "Claims the laptop only crashes when invoices are due."},
		{"Professor Bytewhisker", "+49 000 0002", "bytewhisker@example.invalid", "Brings three devices at once so they can keep each other company."},
		{"Madame Capacita", "+33 000 0003", "capacita@example.invalid", "Requests every screw be returned in a tiny labelled bag."},
		{"Baron von Screwdriver", "+49 000 0004", "screwdriver@example.invalid", "Says the dishwasher is emotionally unstable."},
	},
	repairs: []demoRepair{
		{
			customer: 0, device: "Lappytop Crunchbook 480", issue: "Does not power on; smells like a barbecue started inside the charging port.",
			notes: "Customer says it worked fine until the cat looked at it. The cat denies everything.",
			stageChain: []demoStageStep{
				{"in_progress", false}, {"waiting_parts", false}, {"in_progress", false}, {"repaired", false},
			},
			entries: []demoEntry{
				{"work", "Replaced the crispy charging jack and apologised to the motherboard."},
				{"test", "24h burn-in passed; battery charges to 100% and only complains politely."},
				{"handover", "Collected by Sir Reginald; receipt, cat photo and tiny screw bag handed over."},
			},
			parts: []demoPart{
				{description: "DC-in power jack", qtyMilli: 1000, unit: "pcs", costCents: 1450, priceCents: 3900, notes: "OEM-compatible, unlike the cat."},
				{description: "Thermal paste (syringe)", qtyMilli: 1000, unit: "pcs", costCents: 320, priceCents: 900},
			},
			pickup: &demoPickup{at: "2026-11-09T14:20:00Z", by: "Sir Reginald Solderworth", notes: "Collected with receipt; paid on collection and left a biscuit."},
		},
		{
			customer: 1, device: "Pocket Rectangle 12", issue: "Cracked screen; the lower half of the touchscreen has retired.",
			stageChain: []demoStageStep{{"in_progress", false}},
			entries: []demoEntry{
				{"work", "Screen assembly removed; replacement part on order and pretending to be patient."},
			},
			parts: []demoPart{
				{description: "Screen assembly (OLED)", qtyMilli: 1000, unit: "pcs", costCents: 8900, priceCents: 14900},
			},
		},
		{
			customer: 1, device: "Dishwasher of Doom", issue: "Error E15; water collected in the base pan and formed a tiny swimming pool.",
			notes:      "Customer requests no exorcism, just a repair.",
			stageChain: []demoStageStep{{"waiting_parts", false}},
			entries: []demoEntry{
				{"note", "Dried the base pan; leak sensor still being dramatic."},
				{"contact", "Called customer to approve the sump-hose replacement; approved in three languages."},
			},
			parts: []demoPart{
				{description: "Sump hose", qtyMilli: 2000, unit: "pcs", costCents: 640, priceCents: 1490},
			},
		},
		{
			customer: 2, device: "RGB Toaster XL", issue: "Random reboots under load; fans sound like a tiny helicopter.",
			stageChain: []demoStageStep{{"in_progress", false}, {"lab_test", false}},
			entries: []demoEntry{
				{"test", "Burn-in: three reboots, one existential crisis; power rails look innocent."},
			},
			parts: []demoPart{
				{description: "650 W PSU (80+ Gold)", qtyMilli: 1000, unit: "pcs", costCents: 5200, priceCents: 8900, notes: "Supplier price pending; supplier also pending."},
			},
		},
		{
			customer: 2, device: "Read-o-Matic 3000", issue: "Frozen on the boot logo after an update; the book remains unread.",
			stageChain: []demoStageStep{{"no_repair", false}},
			entries: []demoEntry{
				{"handover", "Board-level sulk; returned unrepaired with no charge and a bookmark."},
			},
		},
		{
			customer: 3, device: "Espresso Rocket 'Morning Zeal'", issue: "No pressure at the group head; pump runs like it is being chased.",
			stageChain: []demoStageStep{{"in_progress", false}, {"customer_test", false}},
			entries: []demoEntry{
				{"work", "Replaced the solenoid valve; descaled the brew circuit and the customer's patience."},
				{"test", "9 bar at the group head; espresso now strong enough to rename the cat."},
			},
			parts: []demoPart{
				{description: "Solenoid valve", qtyMilli: 1000, unit: "pcs", costCents: 2100, priceCents: 4200},
			},
		},
		{
			customer: 3, device: "Silent Disco Headphones", issue: "Right cup silent after water damage; left cup refuses to comment.",
			stageChain: []demoStageStep{{"in_progress", false}},
			entries: []demoEntry{
				{"work", "Cleaned the board, reflowed the right channel and negotiated a truce."},
			},
			parts: []demoPart{
				{description: "Headband assembly", qtyMilli: 1000, unit: "pcs", costCents: 3000, priceCents: 6500, removed: true},
				{description: "Foam ear pads", qtyMilli: 2000, unit: "pcs", costCents: 500, priceCents: 1500},
			},
		},
		{
			customer: 0, device: "Featherbook Airy", issue: "Keyboard repeats keys after a liquid spill; it now has opinions.",
			notes:      "Dropped off this morning; diagnosis not started; customer already apologised to the keyboard.",
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
		{"Nina Nutshell", "+357 00 200001", "nina@example.invalid", "Prefers repair updates by carrier pigeon; accepts email as backup."},
		{"Gadget Gregorian", "+357 00 200002", "gadget@example.invalid", "Tips in biscuits. Biscuits are not legal tender, but appreciated."},
		{"Lola Loopback", "+7 000 200003", "lola@example.invalid", "Believes every cable has a soul and should be untangled gently."},
		{"Mx. Tinker Tumble", "+7 000 200004", "tinker@example.invalid", "Asks for a loyalty card on the first visit."},
		{"Κυρία Καλωδιοπούλου", "+90 000 200005", "kavlodiopoulou@example.invalid", "Wants the device back before the olives are pressed."},
		{"Γιώργος Κατσαβιδόπουλος", "+90 000 200006", "katsavidopoulos@example.invalid", "Calls every device 'the thingamajig'."},
		{"Мадам Проволочкина", "+33 000 200007", "provolochkina@example.invalid", "Requests a receipt in Cyrillic and a summary in mime."},
		{"Товарищ Транзисторов", "+49 000 200008", "tranzistorov@example.invalid", "Maintains the toaster has been plotting since 2019."},
		{"Elif Elektrik", "+34 000 200009", "elif@example.invalid", "Says the phone is fine; the phone disagrees."},
		{"Mehmet Multimetre", "+357 00 200010", "mehmet@example.invalid", "Brings a multimeter to every conversation."},
		{"Sophie Solder", "+48 000 200011", "sophie@example.invalid", "Wants the repair documented in iambic pentameter."},
		{"Lukas Lötkolben", "+44 000 200012", "lukas@example.invalid", "Insists on paying in exact change and unsolicited life advice."},
	}
}

// bulkRepairs generates the rest of the pack. The spread is deliberate: every
// stage appears several times, entries cover all five kinds, and some records
// carry a part with unknown cost, a soft-deleted line, a pickup, and an archived
// state.
func bulkRepairs(customerOffset int) []demoRepair {
	devices := []string{
		"Dusty Lapdesk 5420", "Wishpad 5", "Pro Crumblebook 13", "Galaxophone S21",
		"Pixel Puddle 6", "Reddie Notepad 12", "Flatpad Breeze (2022)", "PlayCrate 5",
		"BoxStation Series S", "Switchy Glowpad", "Glowbox 27\" monitor", "Cannonball SplatPrinter 5750",
		"Sister SnoozePrinter 2350", "Suds-o-Matic washing machine", "Frosty Vault fridge",
		"Crispy Vortex air fryer", "Dust Goblin V11 vacuum", "Mystery Meat Thermometer",
	}
	issues := []string{
		"Makes a noise like a sad kettle when charging.", "Battery lasts exactly one email.",
		"Overheats whenever it sees a spreadsheet.", "Liquid damage; device now speaks in dial-up sounds.",
		"Screen flickers at low brightness and low morale.", "Speakers produce sound only for podcasts about cheese.",
		"Charging port loose; cable falls out when judged.", "Storage full of duplicate photos of the same cat.",
		"Wi-Fi drops whenever the microwave tells a joke.", "Boot loop after an update; device is stuck in its origin story.",
		"Fan noise loud enough to qualify as a leaf blower.", "Dents and a cracked bezel; device lost a fight with a door.",
		"Paper jam every print, especially urgent documents.", "Drum error on start-up; printer is having a percussion crisis.",
		"Water leak during the spin cycle; laundry now includes the floor.", "Compressor runs but does not cool; fridge is on a spiritual journey.",
		"Display shows an error code and refuses to elaborate.", "Battery does not charge; device claims it is on strike.",
	}
	entryText := []string{
		"Diagnosed the fault and ordered the least cursed part", "Cleaned the board and reseated connectors; apologised to both",
		"Ran a burn-in test; device survived, technician almost did", "Explained the options and quoted the repair with a straight face",
		"Replaced the faulty component and the customer's pessimism", "Updated firmware and reset settings to factory-ish",
		"Confirmed the fault is intermittent; device performed on cue", "Checked the power rails and their emotional state",
		"Tested with the customer present; both beeped approvingly", "Prepared the device for collection and found the missing screw",
		"Recorded serial numbers, accessories and one mysterious crumb", "Verified the repair after reassembly; only one screw left over, so we hid it",
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
		{description: "Silicone grease (tube)", qtyMilli: 1000, unit: "pcs", costCents: 180, priceCents: 600},
		{description: "Rubber feet set", qtyMilli: 1000, unit: "pcs", costCents: 120, priceCents: 450},
	}
	out := make([]demoRepair, 0, 36)
	for i := 0; i < 36; i++ {
		s := stagePool[i%len(stagePool)]
		r := demoRepair{
			customer:   customerOffset + i%12,
			device:     fmt.Sprintf("%s Mk.%02d", devices[i%len(devices)], i+1),
			issue:      issues[(i*5)%len(issues)],
			notes:      fmt.Sprintf("Bulk demo repair #%02d; the device demanded equal screen time.", i+1),
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
		if s.stage == "repaired" && i%2 == 0 {
			r.pickup = &demoPickup{
				at:    "2026-11-05T10:00:00Z",
				by:    "A Very Punctual Customer",
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
