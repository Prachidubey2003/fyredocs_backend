package storage

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// Top-level namespaces under the storage root. These align with §4.4.3 of the
// product blueprint: authenticated users get a stable per-user subtree, and
// guests get a per-job subtree under "guests/" since no stable identity exists.
const (
	UserNamespace  = "users"
	GuestNamespace = "guests"
)

// Owner identifies the principal that owns a set of files. Construct it via
// OwnerForUser or OwnerForGuest — never zero-value it.
type Owner struct {
	prefix string // e.g. "users/<uuid>" or "guests/<job-uuid>"
}

func (o Owner) String() string { return o.prefix }

// OwnerForUser returns the on-disk namespace for an authenticated user.
//
//	users/<user_id>/
func OwnerForUser(userID uuid.UUID) Owner {
	return Owner{prefix: filepath.Join(UserNamespace, userID.String())}
}

// OwnerForGuest returns the on-disk namespace for a guest job. Guests have
// no stable identity across requests; the job id partitions their files.
//
//	guests/<job_id>/
func OwnerForGuest(jobID uuid.UUID) Owner {
	return Owner{prefix: filepath.Join(GuestNamespace, jobID.String())}
}

// OwnerFor returns the owner namespace given an optional user id and a
// fallback job id used when userID is nil (guest jobs).
func OwnerFor(userID *uuid.UUID, jobID uuid.UUID) Owner {
	if userID != nil {
		return OwnerForUser(*userID)
	}
	return OwnerForGuest(jobID)
}

// UploadChunkPath returns the on-disk path for a single chunked-upload part.
//
//	<baseDir>/<owner>/uploads/<uploadId>/<index:06d>.part
func UploadChunkPath(baseDir string, owner Owner, uploadID uuid.UUID, chunkIdx int) string {
	return filepath.Join(baseDir, owner.prefix, "uploads", uploadID.String(), fmt.Sprintf("%06d.part", chunkIdx))
}

// UploadAssembledPath returns the on-disk path for the assembled upload.
// The fileName is sanitized so it cannot escape its directory.
//
//	<baseDir>/<owner>/uploads/<uploadId>/<fileName>
func UploadAssembledPath(baseDir string, owner Owner, uploadID uuid.UUID, fileName string) string {
	return filepath.Join(baseDir, owner.prefix, "uploads", uploadID.String(), SafeFileName(fileName))
}

// JobInputPath returns the on-disk path for the input file copied into a job.
//
//	<baseDir>/<owner>/jobs/<jobId>/input/<fileName>
func JobInputPath(baseDir string, owner Owner, jobID uuid.UUID, fileName string) string {
	return filepath.Join(baseDir, owner.prefix, "jobs", jobID.String(), "input", SafeFileName(fileName))
}

// JobScratchDir returns a per-job scratch directory for intermediates.
//
//	<baseDir>/<owner>/jobs/<jobId>/scratch
func JobScratchDir(baseDir string, owner Owner, jobID uuid.UUID) string {
	return filepath.Join(baseDir, owner.prefix, "jobs", jobID.String(), "scratch")
}

// JobOutputPath returns the on-disk path for a worker output file.
//
//	<baseDir>/<owner>/jobs/<jobId>/output/<fileName>
func JobOutputPath(baseDir string, owner Owner, jobID uuid.UUID, fileName string) string {
	return filepath.Join(baseDir, owner.prefix, "jobs", jobID.String(), "output", SafeFileName(fileName))
}

// SafeFileName returns the base name of name with any path separators
// stripped, so it can be appended to a directory without escaping it.
// Returns "_" for empty / current-dir / parent-dir / root inputs.
func SafeFileName(name string) string {
	base := filepath.Base(filepath.Clean(name))
	switch base {
	case "", ".", "..", string(filepath.Separator):
		return "_"
	}
	// Reject any residual separator that survived (Windows paths reaching here).
	if strings.ContainsAny(base, `/\`) {
		return "_"
	}
	return base
}
