package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/tryatlas/cli/internal/capture/store"
	"github.com/tryatlas/cli/internal/project"
)

// Artifacts are the ingest payloads (SPEC §6.7, evidence-capture.md §7.2):
// tagged by "type", camelCase keys, the base fields flattened first. There is
// no author field; the server stamps authorship from the token.

type base struct {
	Type        string `json:"type"`
	RowID       string `json:"rowId"`
	OrgID       string `json:"orgId"`
	WorkspaceID string `json:"workspaceId"`
	Seq         int64  `json:"seq"`
	ContentHash string `json:"contentHash"`
	CreatedAt   string `json:"createdAt"`
}

type sessionArtifact struct {
	base
	SessionID       string          `json:"sessionId"`
	Source          string          `json:"source"`
	NativeSessionID string          `json:"nativeSessionId"`
	Title           *string         `json:"title"`
	Agent           *string         `json:"agent"`
	Model           *string         `json:"model"`
	TokenTotals     json.RawMessage `json:"tokenTotals"`
	StartedAt       string          `json:"startedAt"`
	// Projects is the complete set of the session's tags when its store is
	// a workspace's or a member Project's (ADR-0017): [] clears them. It is
	// absent for a plain Project, which leaves the server's tags alone. It
	// is not in the content hash: the server stores tags before its replay
	// check.
	Projects *[]projectTag `json:"projects,omitempty"`
}

// projectTag is one of a session's tags: another Workspace of its group
// that the session changed files in or committed to. The home Workspace is
// the artifact's own workspaceId, so it has no entry.
type projectTag struct {
	WorkspaceID *string  `json:"workspaceId"` // null while the folder has no cloud Workspace
	Folder      string   `json:"folder"`      // from the workspace folder, with "/"
	Reasons     []string `json:"reasons"`     // edited, committed
}

// tagsFor builds projectTags from a layout's tags.
func tagsFor(tags []project.Tag) *[]projectTag {
	out := make([]projectTag, 0, len(tags))
	for _, t := range tags {
		pt := projectTag{WorkspaceID: t.WorkspaceID, Folder: t.Folder}
		for _, r := range t.Reasons {
			pt.Reasons = append(pt.Reasons, string(r))
		}
		out = append(out, pt)
	}
	return &out
}

type messageArtifact struct {
	base
	SessionID string  `json:"sessionId"`
	TurnSeq   int64   `json:"turnSeq"`
	Role      string  `json:"role"`
	Mode      string  `json:"mode"`
	Preview   string  `json:"preview"`
	Body      *string `json:"body"`
	BodyRef   *string `json:"bodyRef"`
}

type toolCallArtifact struct {
	base
	SessionID    string          `json:"sessionId"`
	TurnSeq      int64           `json:"turnSeq"`
	ToolName     string          `json:"toolName"`
	Title        *string         `json:"title"`
	Kind         *string         `json:"kind"`
	Status       string          `json:"status"`
	Locations    json.RawMessage `json:"locations"`
	Arguments    *string         `json:"arguments"`
	ArgumentsRef *string         `json:"argumentsRef"`
	Result       *string         `json:"result"`
	ResultRef    *string         `json:"resultRef"`
	ResultBinary bool            `json:"resultBinary"`
}

type checkpointArtifact struct {
	base
	SessionID      string          `json:"sessionId"`
	CommitSHA      string          `json:"commitSha"`
	PatchID        *string         `json:"patchId"`
	LinkState      string          `json:"linkState"`
	Branch         *string         `json:"branch"`
	GitAuthorName  *string         `json:"gitAuthorName"`
	GitAuthorEmail *string         `json:"gitAuthorEmail"`
	FilesTouched   json.RawMessage `json:"filesTouched"`
	Insertions     int64           `json:"insertions"`
	Deletions      int64           `json:"deletions"`
}

// Target is where artifacts are filed.
type Target struct {
	OrgID       string
	WorkspaceID string // binding.remote_workspace_id, else binding.slug
}

func (t Target) base(typ, id string, seq int64, hash, created string) base {
	return base{Type: typ, RowID: id, OrgID: t.OrgID, WorkspaceID: t.WorkspaceID, Seq: seq, ContentHash: hash, CreatedAt: created}
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// SessionHash is sha256("{id}:{title}:{agent}:{model}:{token_totals json}").
// A null column formats as the empty string.
func SessionHash(r store.OutSession) string {
	return hashOf(r.ID + ":" + str(r.Title) + ":" + str(r.Agent) + ":" + str(r.Model) + ":" + r.TokenTotals)
}

// ToolCallHash is sha256("{id}:{status}:{result_ref}:{result}").
func ToolCallHash(r store.OutToolCall) string {
	return hashOf(r.ID + ":" + r.Status + ":" + str(r.ResultRef) + ":" + str(r.Result))
}

// CheckpointHash is sha256("{session_id}:{commit_sha}:{link_state}").
func CheckpointHash(r store.OutCheckpoint) string {
	return hashOf(r.SessionID + ":" + r.CommitSHA + ":" + r.LinkState)
}

// item is one row ready to send: its artifact, the blobs it references, and
// the guard that marks it sent.
type item struct {
	kind     store.Kind
	id       string
	artifact json.RawMessage
	blobs    []string
	guard    store.Guard
}

func refs(ps ...*string) []string {
	var out []string
	for _, p := range ps {
		if p != nil && *p != "" {
			out = append(out, *p)
		}
	}
	return out
}

// items turns an outbox page into artifacts, in drain order. tags gives a
// session's projects; nil leaves them out.
func (t Target) items(o store.Outbox, tags func(sessionID string) *[]projectTag) []item {
	var out []item
	add := func(kind store.Kind, id string, a any, g store.Guard, blobs []string) {
		b, err := json.Marshal(a)
		if err != nil {
			return
		}
		out = append(out, item{kind: kind, id: id, artifact: b, blobs: blobs, guard: g})
	}
	for _, r := range o.Sessions {
		add(store.KindSession, r.ID, sessionArtifact{
			base:      t.base("agent_session", r.ID, 0, SessionHash(r), r.StartedAt),
			SessionID: r.ID, Source: r.Source, NativeSessionID: r.NativeID, Title: r.Title, Agent: r.Agent, Model: r.Model,
			TokenTotals: store.RawJSON(r.TokenTotals, "{}"), StartedAt: r.StartedAt, Projects: tags(r.ID),
		}, store.SessionGuard(r), nil)
	}
	for _, r := range o.Messages {
		add(store.KindMessage, r.ID, messageArtifact{
			base:      t.base("agent_message", r.ID, r.Seq, r.ContentHash, r.CreatedAt),
			SessionID: r.SessionID, TurnSeq: r.TurnSeq, Role: r.Role, Mode: r.Mode, Preview: r.Preview, Body: r.Body, BodyRef: r.BodyRef,
		}, store.MessageGuard(r), refs(r.BodyRef))
	}
	for _, r := range o.ToolCalls {
		add(store.KindToolCall, r.ID, toolCallArtifact{
			base:      t.base("tool_call", r.ID, r.Seq, ToolCallHash(r), r.CreatedAt),
			SessionID: r.SessionID, TurnSeq: r.TurnSeq, ToolName: r.ToolName, Title: r.Title, Kind: r.Kind, Status: r.Status,
			Locations: store.RawJSON(r.Locations, "[]"), Arguments: r.Arguments, ArgumentsRef: r.ArgumentsRef,
			Result: r.Result, ResultRef: r.ResultRef, ResultBinary: r.ResultBinary,
		}, store.ToolCallGuard(r), refs(r.ArgumentsRef, r.ResultRef))
	}
	for _, r := range o.Checkpoints {
		add(store.KindCheckpoint, r.ID, checkpointArtifact{
			base:      t.base("checkpoint", r.ID, 0, CheckpointHash(r), r.CreatedAt),
			SessionID: r.SessionID, CommitSHA: r.CommitSHA, PatchID: r.PatchID, LinkState: r.LinkState, Branch: r.Branch,
			GitAuthorName: r.AuthorName, GitAuthorEmail: r.AuthorEmail, FilesTouched: store.RawJSON(r.FilesTouched, "[]"),
			Insertions: r.Insertions, Deletions: r.Deletions,
		}, store.CheckpointGuard(r), nil)
	}
	return out
}
