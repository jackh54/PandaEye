package identity

import (
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// verdictNamespace is the UUIDv5 namespace of verdict ids. Changing it changes every verdict id.
var verdictNamespace = uuid.MustParse("6f2d9a4e-1c3b-5e7f-8a90-b1c2d3e4f5a6")

// VerdictID returns the deterministic verdict id
// UUIDv5(namespace, tenant | player | session | action | triggering signal stream seq), so that
// re-processing the same signals produces the same verdict (ARCHITECTURE 5.6).
func VerdictID(tenantID string, player UUID, sessionID string, action int32, signalSeq uint64) string {
	name := strings.Join([]string{
		tenantID,
		player.String(),
		sessionID,
		strconv.FormatInt(int64(action), 10),
		strconv.FormatUint(signalSeq, 10),
	}, "|")
	return uuid.NewSHA1(verdictNamespace, []byte(name)).String()
}

// DerivedVerdictID returns the deterministic id of a corrective verdict (UNBAN, UNMITIGATE) created
// for parentID, so retried overturns create the verdict only once.
func DerivedVerdictID(parentID string, action int32) string {
	name := "derived|" + parentID + "|" + strconv.FormatInt(int64(action), 10)
	return uuid.NewSHA1(verdictNamespace, []byte(name)).String()
}
