package agent

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const RunInputManifestSchemaVersion = "kupilot.run-input-manifest/v1"

// RunInputManifest is a content-free digest of the initial input and every
// committed steer accepted for the current run.
type RunInputManifest struct {
	Count  int
	Digest string
}

// BuildRunInputManifest creates the same deterministic projection at the
// Application and Eino boundaries without copying input content.
func BuildRunInputManifest(input RunInput, steers []ConversationTurn) (RunInputManifest, error) {
	if input.Validate() != nil || len(steers) > domain.MaxCommittedSteerInputs {
		return RunInputManifest{}, ErrInvalidRunInput
	}
	hash := sha256.New()
	writeManifestField := func(value string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(value))
	}
	writeManifestField(RunInputManifestSchemaVersion)
	writeManifestField(string(input.SessionID()))
	writeManifestField(string(input.RunID()))
	writeManifestField(string(input.RequestMessageID()))
	writeManifestField("0")
	writeManifestField(domain.MessageContentHash(input.Question()))
	for index, steer := range steers {
		if steer.validate() != nil || steer.RunID != input.RunID() || steer.Role != domain.MessageRoleUser ||
			steer.RunSequence != index+1 {
			return RunInputManifest{}, ErrInvalidRunInput
		}
		writeManifestField(string(steer.MessageID))
		writeManifestField(string(steer.RunID))
		writeManifestField(strconv.Itoa(steer.RunSequence))
		writeManifestField(steer.ContentHash)
	}
	return RunInputManifest{Count: 1 + len(steers), Digest: hex.EncodeToString(hash.Sum(nil))}, nil
}
