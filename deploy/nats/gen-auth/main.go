// Command gen-auth issues NATS decentralised (operator / JWT) authentication for an external NATS
// used by split PandaEye services: one operator, the SYS and PANDAEYE accounts, and one user per role
// with least-privilege subject permissions. PandaEye services authenticate with PE_NATS_CREDS (the
// configuration rejects credentials inside PE_NATS_URL, and a token cannot carry per-role
// permissions), so per-role isolation needs creds files.
//
//	go run . -out ../auth                     first run: new keys, resolver.conf, every user's creds
//	go run . -out ../auth                     later runs: keep everything, rewrite resolver.conf
//	go run . -out ../auth -reissue pe-worker  new creds for some users (or "all"); old ones stay valid
//	go run . -out ../auth -revoke-old         revoke every superseded user key (then reload nats-server)
//	go run . -print                           print the per-role permissions and exit
//
// Output (secrets: keep it out of version control; deploy/.gitignore excludes nats/auth/):
//
//	server/resolver.conf  included by nats-server.conf: operator JWT, system_account, MEMORY resolver
//	                      (public JWTs only; mount the server/ directory, not the file, so a
//	                      regenerated file is visible to a running container after a reload)
//	creds/<user>.creds    PE_NATS_CREDS of each role (the user's JWT and seed)
//	keys/                 operator and account seeds, the operator JWT and the issued user keys
//	                      (users.json); needed to re-issue or revoke, never mounted into a container
//
// Permissions are derived in docs/OPERATIONS.md ("NATS permissions") from ARCHITECTURE 5.3-5.8.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// Streams and KV buckets of ARCHITECTURE 5.4.
const (
	streamEvents   = "PE_EVENTS"
	streamSignals  = "PE_SIGNALS"
	streamVerdicts = "PE_VERDICTS"
	kvLeases       = "PE_LEASES"
	kvConnections  = "PE_CONNECTIONS"
	kvServers      = "PE_SERVERS"
	kvTenants      = "PE_TENANTS"
	kvMeta         = "PE_META"
)

// role is one NATS user of the PANDAEYE account.
type role struct {
	user    string
	purpose string
	pub     []string
	sub     []string
}

// stream returns the stream-level API a role needs on a stream it publishes to or consumes. CREATE and
// UPDATE are included because the spec does not fix which process provisions streams on an external
// cluster; no role can delete or purge a stream.
func stream(name string) []string {
	return []string{
		"$JS.API.STREAM.INFO." + name,
		"$JS.API.STREAM.CREATE." + name,
		"$JS.API.STREAM.UPDATE." + name,
	}
}

// consume returns the consumer API for durable pull consumers and ordered (ephemeral) consumers on a
// stream: "$JS.API.CONSUMER.*.<stream>[.>]" covers CREATE, INFO, DELETE, PAUSE, UNPIN, RESET, LIST and
// NAMES; acks are listed in the v1 and v2 ack-subject formats.
func consume(name string) []string {
	return []string{
		"$JS.API.CONSUMER.*." + name,
		"$JS.API.CONSUMER.*." + name + ".>",
		"$JS.API.CONSUMER.DURABLE.CREATE." + name + ".>",
		"$JS.API.CONSUMER.MSG.NEXT." + name + ".>",
		"$JS.ACK." + name + ".>",
		"$JS.ACK.*.*." + name + ".>",
	}
}

// kv returns bucket access: bind/provision, get (direct and stream get), watch and key listing (push
// ordered consumers with flow control), and with write also put/create/update/delete of keys.
func kv(bucket string, write bool) []string {
	s := "KV_" + bucket
	out := []string{
		"$JS.API.STREAM.INFO." + s,
		"$JS.API.STREAM.CREATE." + s,
		"$JS.API.STREAM.UPDATE." + s,
		"$JS.API.STREAM.MSG.GET." + s,
		"$JS.API.DIRECT.GET." + s,
		"$JS.API.DIRECT.GET." + s + ".>",
		"$JS.API.CONSUMER.*." + s,
		"$JS.API.CONSUMER.*." + s + ".>",
		"$JS.FC." + s + ".>",
		"$JS.FC.*.*." + s + ".>",
	}
	if write {
		out = append(out, "$KV."+bucket+".>")
	}
	return out
}

func join(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		for _, s := range p {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

var inbox = []string{"_INBOX.>"}
var accountInfo = []string{"$JS.API.INFO"}

// roles is the authoritative permission table.
var roles = []role{
	{
		user:    "pe-gateway",
		purpose: "gateway: publishes StreamBatch and AckRecord, owns the connection registry, receives SignedOutbound",
		pub: join(
			[]string{"pe.events.*", "pe.acks.*"},
			accountInfo,
			stream(streamEvents), stream(streamVerdicts),
			kv(kvConnections, true), kv(kvServers, true), kv(kvMeta, true), kv(kvTenants, false),
		),
		sub: join(inbox, []string{"pe.out.>"}),
	},
	{
		user:    "pe-worker",
		purpose: "worker: consumes PE_EVENTS, publishes signals and session markers, holds partition leases",
		pub: join(
			[]string{"pe.signals.*"},
			accountInfo,
			stream(streamEvents), consume(streamEvents), stream(streamSignals),
			kv(kvLeases, true), kv(kvMeta, true), kv(kvTenants, false),
		),
		sub: inbox,
	},
	{
		user:    "pe-verdict",
		purpose: "verdict service and store writer: consumes PE_SIGNALS and PE_VERDICTS, publishes VerdictRecord and SignedOutbound",
		pub: join(
			[]string{"pe.verdicts.*", "pe.out.*.*"},
			accountInfo,
			stream(streamSignals), consume(streamSignals), stream(streamVerdicts), consume(streamVerdicts),
			kv(kvLeases, true), kv(kvMeta, true), kv(kvTenants, false), kv(kvConnections, false),
		),
		sub: inbox,
	},
	{
		user:    "pe-api",
		purpose: "api: writes tenant settings, publishes config updates and corrective verdicts, runs leased loops",
		pub: join(
			[]string{"pe.out.*.*", "pe.verdicts.*"},
			accountInfo,
			stream(streamVerdicts),
			kv(kvTenants, true), kv(kvLeases, true), kv(kvConnections, false), kv(kvServers, false),
		),
		sub: inbox,
	},
	{
		user:    "pe-replay",
		purpose: "`pandaeye replay`: reads PE_EVENTS with ephemeral consumers (consumer rights on PE_EVENTS include deleting consumers)",
		pub: join(
			accountInfo,
			[]string{
				"$JS.API.STREAM.INFO." + streamEvents,
				"$JS.API.STREAM.MSG.GET." + streamEvents,
				"$JS.API.DIRECT.GET." + streamEvents,
				"$JS.API.DIRECT.GET." + streamEvents + ".>",
			},
			consume(streamEvents),
			[]string{
				"$JS.API.STREAM.INFO.KV_" + kvMeta,
				"$JS.API.STREAM.MSG.GET.KV_" + kvMeta,
				"$JS.API.DIRECT.GET.KV_" + kvMeta,
				"$JS.API.DIRECT.GET.KV_" + kvMeta + ".>",
			},
		),
		sub: inbox,
	},
	{
		user:    "pe-admin",
		purpose: "operators: provisioning, backups and the nats CLI; full access inside the PANDAEYE account, never given to a service",
		pub:     []string{">"},
		sub:     []string{">"},
	},
}

// state is keys/users.json: the user keys issued so far.
type state struct {
	Users map[string]*userKeys `json:"users"`
}

type userKeys struct {
	Current  string           `json:"current"`
	Previous []string         `json:"previous,omitempty"`
	Revoked  map[string]int64 `json:"revoked,omitempty"` // public key -> unix seconds
}

func main() {
	out := flag.String("out", "auth", "output directory")
	printOnly := flag.Bool("print", false, "print the per-role permissions and exit")
	reissue := flag.String("reissue", "", "comma-separated users to issue new creds for, or \"all\"")
	revokeOld := flag.Bool("revoke-old", false, "revoke every superseded user key")
	flag.Parse()

	if *printOnly {
		printPermissions()
		return
	}
	if err := run(*out, *reissue, *revokeOld); err != nil {
		fmt.Fprintln(os.Stderr, "gen-auth:", err)
		os.Exit(1)
	}
}

func printPermissions() {
	for _, r := range roles {
		fmt.Printf("# %s: %s\npublish allow:\n", r.user, r.purpose)
		for _, s := range r.pub {
			fmt.Printf("  %s\n", s)
		}
		fmt.Println("subscribe allow:")
		for _, s := range r.sub {
			fmt.Printf("  %s\n", s)
		}
		fmt.Println()
	}
}

func run(out, reissue string, revokeOld bool) error {
	keysDir := filepath.Join(out, "keys")
	credsDir := filepath.Join(out, "creds")
	serverDir := filepath.Join(out, "server")
	for _, d := range []string{out, keysDir, credsDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		return err
	}

	operator, err := loadOrCreate(filepath.Join(keysDir, "operator.nk"), nkeys.CreateOperator)
	if err != nil {
		return err
	}
	sysAcct, err := loadOrCreate(filepath.Join(keysDir, "SYS.nk"), nkeys.CreateAccount)
	if err != nil {
		return err
	}
	peAcct, err := loadOrCreate(filepath.Join(keysDir, "PANDAEYE.nk"), nkeys.CreateAccount)
	if err != nil {
		return err
	}
	st, err := loadState(filepath.Join(keysDir, "users.json"))
	if err != nil {
		return err
	}

	want := map[string]bool{}
	for _, u := range strings.Split(reissue, ",") {
		if u = strings.TrimSpace(u); u != "" {
			want[u] = true
		}
	}
	for u := range want {
		if u != "all" && !slices.ContainsFunc(roles, func(r role) bool { return r.user == u }) {
			return fmt.Errorf("-reissue: unknown user %q", u)
		}
	}

	// Users: issue creds for new users and for users named in -reissue.
	type issued struct {
		name string
		kp   nkeys.KeyPair
		acct nkeys.KeyPair
		r    *role
	}
	var toIssue []issued
	for i := range roles {
		r := &roles[i]
		uk := st.Users[r.user]
		credsMissing := !exists(filepath.Join(credsDir, r.user+".creds"))
		if uk == nil || credsMissing || want["all"] || want[r.user] {
			kp, err := nkeys.CreateUser()
			if err != nil {
				return err
			}
			pub, _ := kp.PublicKey()
			if uk == nil {
				uk = &userKeys{}
				st.Users[r.user] = uk
			}
			if uk.Current != "" {
				uk.Previous = append(uk.Previous, uk.Current)
			}
			uk.Current = pub
			toIssue = append(toIssue, issued{name: r.user, kp: kp, acct: peAcct, r: r})
		}
	}
	sysUser := st.Users["sys"]
	if sysUser == nil || !exists(filepath.Join(credsDir, "sys.creds")) || want["all"] {
		kp, err := nkeys.CreateUser()
		if err != nil {
			return err
		}
		pub, _ := kp.PublicKey()
		if sysUser == nil {
			sysUser = &userKeys{}
			st.Users["sys"] = sysUser
		}
		if sysUser.Current != "" {
			sysUser.Previous = append(sysUser.Previous, sysUser.Current)
		}
		sysUser.Current = pub
		toIssue = append(toIssue, issued{name: "sys", kp: kp, acct: sysAcct})
	}

	now := time.Now().Unix()
	if revokeOld {
		for _, uk := range st.Users {
			for _, p := range uk.Previous {
				if uk.Revoked == nil {
					uk.Revoked = map[string]int64{}
				}
				if _, ok := uk.Revoked[p]; !ok {
					uk.Revoked[p] = now
				}
			}
			uk.Previous = nil
		}
	}

	opub, _ := operator.PublicKey()
	spub, _ := sysAcct.PublicKey()
	apub, _ := peAcct.PublicKey()

	// The operator JWT is issued once and reused: nats-server cannot reload a changed operator, while
	// changed account JWTs (new users' revocations) apply on a reload.
	operatorJWT, err := loadOrIssueOperator(filepath.Join(keysDir, "operator.jwt"), operator, opub, spub)
	if err != nil {
		return err
	}

	sc := jwt.NewAccountClaims(spub)
	sc.Name = "SYS"
	revokeInto(sc, st, "sys")
	sysJWT, err := sc.Encode(operator)
	if err != nil {
		return fmt.Errorf("SYS account jwt: %w", err)
	}

	ac := jwt.NewAccountClaims(apub)
	ac.Name = "PANDAEYE"
	ac.Limits.JetStreamLimits = jwt.JetStreamLimits{
		MemoryStorage: jwt.NoLimit, DiskStorage: jwt.NoLimit, Streams: jwt.NoLimit, Consumer: jwt.NoLimit,
		MaxAckPending: jwt.NoLimit, MemoryMaxStreamBytes: jwt.NoLimit, DiskMaxStreamBytes: jwt.NoLimit,
	}
	for _, r := range roles {
		revokeInto(ac, st, r.user)
	}
	accountJWT, err := ac.Encode(operator)
	if err != nil {
		return fmt.Errorf("PANDAEYE account jwt: %w", err)
	}

	for _, is := range toIssue {
		pub, _ := is.kp.PublicKey()
		uc := jwt.NewUserClaims(pub)
		uc.Name = is.name
		if is.r != nil {
			uc.Pub.Allow.Add(is.r.pub...)
			uc.Sub.Allow.Add(is.r.sub...)
		}
		userJWT, err := uc.Encode(is.acct)
		if err != nil {
			return fmt.Errorf("user %s jwt: %w", is.name, err)
		}
		seed, err := is.kp.Seed()
		if err != nil {
			return err
		}
		creds, err := jwt.FormatUserConfig(userJWT, seed)
		if err != nil {
			return err
		}
		// 0644: the distroless nonroot user (uid 65532) must read the bind-mounted file; the 0700
		// parent directory keeps other host users out.
		if err := writeFile(filepath.Join(credsDir, is.name+".creds"), creds, 0o644); err != nil {
			return err
		}
	}

	resolver := fmt.Sprintf(`# Generated by deploy/nats/gen-auth on %s. Public JWTs only; regenerate, do not edit.
operator: %s
system_account: %s
resolver: MEMORY
resolver_preload: {
  # SYS
  %s: %s
  # PANDAEYE
  %s: %s
}
`, time.Now().UTC().Format(time.RFC3339), operatorJWT, spub, spub, sysJWT, apub, accountJWT)
	if err := writeFile(filepath.Join(serverDir, "resolver.conf"), []byte(resolver), 0o644); err != nil {
		return err
	}
	if err := saveState(filepath.Join(keysDir, "users.json"), st); err != nil {
		return err
	}

	names := make([]string, 0, len(toIssue))
	for _, is := range toIssue {
		names = append(names, is.name)
	}
	sort.Strings(names)
	fmt.Printf("wrote %s; issued creds: %s\n", filepath.Join(serverDir, "resolver.conf"), strings.Join(names, ", "))
	if len(names) == 0 {
		fmt.Println("no creds issued (use -reissue to rotate)")
	}
	if revokeOld {
		fmt.Println("superseded keys revoked: reload every nats-server (nats-server --signal reload, or SIGHUP) to apply")
	}
	return nil
}

func loadOrIssueOperator(path string, operator nkeys.KeyPair, opub, spub string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		token := strings.TrimSpace(string(data))
		oc, err := jwt.DecodeOperatorClaims(token)
		if err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		if oc.Subject != opub || oc.SystemAccount != spub {
			return "", fmt.Errorf("%s does not match keys/operator.nk and keys/SYS.nk", path)
		}
		return token, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	oc := jwt.NewOperatorClaims(opub)
	oc.Name = "pandaeye"
	oc.SystemAccount = spub
	token, err := oc.Encode(operator)
	if err != nil {
		return "", fmt.Errorf("operator jwt: %w", err)
	}
	return token, writeFile(path, []byte(token+"\n"), 0o600)
}

func revokeInto(ac *jwt.AccountClaims, st *state, user string) {
	uk := st.Users[user]
	if uk == nil {
		return
	}
	for pub, at := range uk.Revoked {
		ac.RevokeAt(pub, time.Unix(at, 0))
	}
}

func loadOrCreate(path string, create func() (nkeys.KeyPair, error)) (nkeys.KeyPair, error) {
	if seed, err := os.ReadFile(path); err == nil {
		return nkeys.FromSeed([]byte(strings.TrimSpace(string(seed))))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	kp, err := create()
	if err != nil {
		return nil, err
	}
	seed, err := kp.Seed()
	if err != nil {
		return nil, err
	}
	return kp, writeFile(path, append(seed, '\n'), 0o600)
}

func loadState(path string) (*state, error) {
	st := &state{Users: map[string]*userKeys{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if st.Users == nil {
		st.Users = map[string]*userKeys{}
	}
	return st, nil
}

func saveState(path string, st *state) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(data, '\n'), 0o600)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writeFile replaces path atomically with the given mode.
func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
