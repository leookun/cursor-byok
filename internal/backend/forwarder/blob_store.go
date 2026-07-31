package forwarder

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"cursor/gen/agentv1"
)

const (
	conversationBlobDirectoryName   = ".blobs"
	conversationBlobMaxItemBytes    = 64 << 20
	conversationBlobMaxRequestBytes = 256 << 20
	conversationBlobMaxRequestItems = 1000
	conversationPrefetchMaxBytes    = 32 << 20
	conversationPrefetchMaxItems    = 512
	conversationBidiMaxRequestBytes = (conversationBlobMaxItemBytes * 2) + (16 << 20)
	conversationBlobMaxStoreBytes   = int64(4 << 30)
	conversationBlobGCGracePeriod   = 7 * 24 * time.Hour
	conversationBlobAckTimeout      = 10 * time.Second
	conversationBlobAckBatchTimeout = 30 * time.Second
	conversationBlobImportIdleTime  = 10 * time.Second
	conversationBlobImportPoll      = 250 * time.Millisecond
	conversationBlobGetMaxInFlight  = 128
	conversationBlobRetryBase       = time.Second
	conversationBlobRetryMax        = 30 * time.Second
	completedKVAckRetention         = 10 * time.Minute
)

var (
	errConversationBlobStoreFull = errors.New("conversation blob store is full")
	errConversationBlobCorrupt   = errors.New("conversation blob is corrupt")
)

type ConversationBlobStore struct {
	mu        sync.RWMutex
	root      string
	memory    map[[sha256.Size]byte][]byte
	verified  map[[sha256.Size]byte]struct{}
	totalSize int64
	sizeKnown bool
}

func NewConversationBlobStore(historyRoot string) *ConversationBlobStore {
	root := ""
	if strings.TrimSpace(historyRoot) != "" {
		root = filepath.Join(strings.TrimSpace(historyRoot), conversationBlobDirectoryName)
	}
	return &ConversationBlobStore{
		root:     root,
		memory:   make(map[[sha256.Size]byte][]byte),
		verified: make(map[[sha256.Size]byte]struct{}),
	}
}

func (store *ConversationBlobStore) Put(id []byte, data []byte) error {
	return store.put(id, data)
}

// PutProjected persists a blob produced from trusted local history. Digests
// already verified by this process need no repeated full-file validation.
func (store *ConversationBlobStore) PutProjected(id []byte, data []byte) error {
	digest, err := parseBlobDigest(id)
	if err != nil {
		return err
	}
	if len(data) > conversationBlobMaxItemBytes {
		return fmt.Errorf("conversation blob exceeds %d byte limit", conversationBlobMaxItemBytes)
	}
	store.mu.RLock()
	_, verified := store.verified[digest]
	store.mu.RUnlock()
	if verified {
		return nil
	}
	return store.put(id, data)
}

func (store *ConversationBlobStore) put(id []byte, data []byte) error {
	digest, err := validatedBlobDigest(id, data)
	if err != nil {
		return err
	}
	if len(data) > conversationBlobMaxItemBytes {
		return fmt.Errorf("conversation blob exceeds %d byte limit", conversationBlobMaxItemBytes)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.root == "" {
		if existing, ok := store.memory[digest]; ok {
			if !bytes.Equal(existing, data) {
				return fmt.Errorf("conversation blob collision for %s", hex.EncodeToString(digest[:]))
			}
			store.verified[digest] = struct{}{}
			return nil
		}
		if store.totalSize+int64(len(data)) > conversationBlobMaxStoreBytes {
			return fmt.Errorf("%w: exceeds %d byte limit", errConversationBlobStoreFull, conversationBlobMaxStoreBytes)
		}
		store.memory[digest] = append([]byte(nil), data...)
		store.verified[digest] = struct{}{}
		store.totalSize += int64(len(data))
		store.sizeKnown = true
		return nil
	}
	if err := os.MkdirAll(store.root, 0o755); err != nil {
		return fmt.Errorf("create conversation blob directory: %w", err)
	}
	path := filepath.Join(store.root, hex.EncodeToString(digest[:]))
	if info, err := os.Stat(path); err == nil {
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read conversation blob: %w", readErr)
		}
		if _, validationErr := validatedBlobDigest(id, existing); validationErr == nil {
			if !bytes.Equal(existing, data) {
				return fmt.Errorf("conversation blob collision for %s", hex.EncodeToString(digest[:]))
			}
			now := time.Now()
			if err := os.Chtimes(path, now, now); err != nil {
				return fmt.Errorf("refresh conversation blob lease: %w", err)
			}
			store.verified[digest] = struct{}{}
			return nil
		}
		if err := store.loadSizeLocked(); err != nil {
			return err
		}
		replacementSize := store.totalSize - info.Size() + int64(len(data))
		if replacementSize > conversationBlobMaxStoreBytes {
			return fmt.Errorf("%w: exceeds %d byte limit", errConversationBlobStoreFull, conversationBlobMaxStoreBytes)
		}
		if err := writeConversationBlobFile(store.root, path, data, true); err != nil {
			return err
		}
		store.totalSize = replacementSize
		store.verified[digest] = struct{}{}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read conversation blob: %w", err)
	}
	if err := store.loadSizeLocked(); err != nil {
		return err
	}
	if store.totalSize+int64(len(data)) > conversationBlobMaxStoreBytes {
		return fmt.Errorf("%w: exceeds %d byte limit", errConversationBlobStoreFull, conversationBlobMaxStoreBytes)
	}
	if err := writeConversationBlobFile(store.root, path, data, false); err != nil {
		return err
	}
	store.totalSize += int64(len(data))
	store.verified[digest] = struct{}{}
	return nil
}

func writeConversationBlobFile(root string, path string, data []byte, replace bool) error {
	temporary, err := os.CreateTemp(root, ".blob-*")
	if err != nil {
		return fmt.Errorf("create temporary conversation blob: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary conversation blob: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary conversation blob: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		if !replace {
			return fmt.Errorf("commit conversation blob: %w", err)
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("replace corrupt conversation blob: %w", removeErr)
		}
		if retryErr := os.Rename(temporaryPath, path); retryErr != nil {
			return fmt.Errorf("commit repaired conversation blob: %w", retryErr)
		}
	}
	removeTemporary = false
	return nil
}

func (store *ConversationBlobStore) Get(id []byte) ([]byte, error) {
	digest, err := parseBlobDigest(id)
	if err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.root == "" {
		data, ok := store.memory[digest]
		if !ok {
			return nil, os.ErrNotExist
		}
		return append([]byte(nil), data...), nil
	}
	data, err := os.ReadFile(filepath.Join(store.root, hex.EncodeToString(digest[:])))
	if err != nil {
		delete(store.verified, digest)
		return nil, err
	}
	if _, err := validatedBlobDigest(id, data); err != nil {
		delete(store.verified, digest)
		return nil, fmt.Errorf("%w: %v", errConversationBlobCorrupt, err)
	}
	store.verified[digest] = struct{}{}
	return data, nil
}

func (store *ConversationBlobStore) Prune(reachable map[[sha256.Size]byte]struct{}, cutoff time.Time) error {
	if store == nil || store.root == "" {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	entries, err := os.ReadDir(store.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			store.totalSize = 0
			store.sizeKnown = true
			return nil
		}
		return fmt.Errorf("scan conversation blob directory: %w", err)
	}
	var totalSize int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(store.root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("read conversation blob info: %w", err)
		}
		if strings.HasPrefix(entry.Name(), ".blob-") {
			if info.ModTime().Before(cutoff) {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("remove stale temporary conversation blob: %w", err)
				}
				continue
			}
			totalSize += info.Size()
			continue
		}
		digest, ok := parseBlobFileName(entry.Name())
		if !ok {
			totalSize += info.Size()
			continue
		}
		if _, keep := reachable[digest]; !keep && info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove unreachable conversation blob: %w", err)
			}
			delete(store.verified, digest)
			continue
		}
		totalSize += info.Size()
	}
	store.totalSize = totalSize
	store.sizeKnown = true
	return nil
}

func (store *ConversationBlobStore) loadSizeLocked() error {
	if store.sizeKnown || store.root == "" {
		return nil
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			store.sizeKnown = true
			return nil
		}
		return fmt.Errorf("scan conversation blob directory: %w", err)
	}
	var totalSize int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("read conversation blob info: %w", err)
		}
		totalSize += info.Size()
	}
	store.totalSize = totalSize
	store.sizeKnown = true
	return nil
}

func parseBlobFileName(name string) ([sha256.Size]byte, bool) {
	var digest [sha256.Size]byte
	if len(name) != sha256.Size*2 {
		return digest, false
	}
	decoded, err := hex.DecodeString(name)
	if err != nil || len(decoded) != sha256.Size {
		return digest, false
	}
	copy(digest[:], decoded)
	return digest, true
}

func parseBlobDigest(id []byte) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if len(id) != sha256.Size {
		return digest, fmt.Errorf("blob id must contain %d bytes", sha256.Size)
	}
	copy(digest[:], id)
	return digest, nil
}

func validatedBlobDigest(id []byte, data []byte) ([sha256.Size]byte, error) {
	digest, err := parseBlobDigest(id)
	if err != nil {
		return digest, err
	}
	computed := sha256.Sum256(data)
	if computed != digest {
		return digest, fmt.Errorf("blob id does not match SHA-256 content digest")
	}
	return digest, nil
}

func (service *Service) UploadConversationBlobs(_ context.Context, req *connect.Request[agentv1.UploadConversationBlobsRequest]) (*connect.Response[agentv1.UploadConversationBlobsResponse], error) {
	if service == nil || service.blobStore == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("conversation blob store is not available"))
	}
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request payload is required"))
	}
	if _, err := validateConversationID(req.Msg.GetConversationId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := validateUploadChunk(req.Msg.GetChunkIndex(), req.Msg.GetTotalChunks()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := validateBlobBatch(req.Msg.GetBlobs()); err != nil {
		return nil, connect.NewError(connect.CodeResourceExhausted, err)
	}
	for index, blob := range req.Msg.GetBlobs() {
		if blob == nil {
			continue
		}
		if err := service.putConversationBlobWithRecovery(blob.GetId(), blob.GetValue()); err != nil {
			code := connect.CodeInvalidArgument
			if errors.Is(err, errConversationBlobStoreFull) {
				code = connect.CodeResourceExhausted
			}
			return nil, connect.NewError(code, fmt.Errorf("store uploaded conversation blob %d: %w", index, err))
		}
	}
	return connect.NewResponse(&agentv1.UploadConversationBlobsResponse{}), nil
}

func (service *Service) NotifyConversationClone(ctx context.Context, req *connect.Request[agentv1.NotifyConversationCloneRequest]) (*connect.Response[agentv1.NotifyConversationCloneResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request payload is required"))
	}
	destinationID, err := validateConversationID(req.Msg.GetConversationId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	sourceID, err := validateConversationID(req.Msg.GetSourceConversationId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if destinationID == sourceID {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("clone destination must differ from source conversation"))
	}
	if service == nil || service.store == nil || service.projector == nil || service.blobStore == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("conversation clone projection is not available"))
	}
	if err := ctx.Err(); err != nil {
		return nil, connect.NewError(connect.CodeCanceled, err)
	}
	source, err := service.store.LoadConversation(sourceID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load clone source conversation: %w", err))
	}
	if source == nil {
		return connect.NewResponse(&agentv1.NotifyConversationCloneResponse{}), nil
	}
	projection, err := service.projector.ProjectLegacyCheckpointWithBlobs(source)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("project clone source conversation: %w", err))
	}
	if err := service.persistCheckpointBlobs(projection.Blobs); err != nil {
		code := connect.CodeInternal
		if errors.Is(err, errConversationBlobStoreFull) {
			code = connect.CodeResourceExhausted
		}
		return nil, connect.NewError(code, fmt.Errorf("persist clone source blobs: %w", err))
	}
	return connect.NewResponse(&agentv1.NotifyConversationCloneResponse{}), nil
}

func validateUploadChunk(chunkIndex int32, totalChunks int32) error {
	if chunkIndex < 0 || totalChunks < 0 {
		return fmt.Errorf("upload chunk indexes must be non-negative")
	}
	if totalChunks == 0 {
		if chunkIndex != 0 {
			return fmt.Errorf("chunk_index must be zero when total_chunks is zero")
		}
		return nil
	}
	if chunkIndex >= totalChunks {
		return fmt.Errorf("chunk_index must be less than total_chunks")
	}
	return nil
}

func validateBlobBatch(blobs []*agentv1.BlobEntry) error {
	if len(blobs) > conversationBlobMaxRequestItems {
		return fmt.Errorf("conversation blob upload exceeds %d item limit", conversationBlobMaxRequestItems)
	}
	var totalBytes int64
	for _, blob := range blobs {
		if blob == nil {
			continue
		}
		if len(blob.GetValue()) > conversationBlobMaxItemBytes {
			return fmt.Errorf("conversation blob exceeds %d byte limit", conversationBlobMaxItemBytes)
		}
		totalBytes += int64(len(blob.GetValue()))
		if totalBytes > conversationBlobMaxRequestBytes {
			return fmt.Errorf("conversation blob upload exceeds %d byte limit", conversationBlobMaxRequestBytes)
		}
	}
	return nil
}

func (service *Service) persistPreFetchedBlobs(blobs []*agentv1.PreFetchedBlob) error {
	if len(blobs) == 0 {
		return nil
	}
	if service == nil || service.blobStore == nil {
		return fmt.Errorf("conversation blob store is not available")
	}
	if len(blobs) > conversationPrefetchMaxItems {
		return fmt.Errorf("pre-fetched blobs exceed %d item limit", conversationPrefetchMaxItems)
	}
	var totalBytes int64
	for index, blob := range blobs {
		if blob == nil {
			continue
		}
		totalBytes += int64(len(blob.GetValue()))
		if len(blob.GetValue()) > conversationBlobMaxItemBytes || totalBytes > conversationPrefetchMaxBytes {
			return fmt.Errorf("pre-fetched blobs exceed capacity limit")
		}
		if err := service.putConversationBlobWithRecovery(blob.GetId(), blob.GetValue()); err != nil {
			return fmt.Errorf("store pre-fetched conversation blob %d: %w", index, err)
		}
	}
	return nil
}

func (service *Service) persistCheckpointBlobs(blobs []CheckpointBlob) error {
	if len(blobs) == 0 {
		return nil
	}
	if service == nil || service.blobStore == nil {
		return fmt.Errorf("conversation blob store is not available")
	}
	for _, blob := range blobs {
		if err := service.putProjectedConversationBlobWithRecovery(blob.ID, blob.Data); err != nil {
			return fmt.Errorf("persist checkpoint blob: %w", err)
		}
	}
	return nil
}

func (service *Service) putConversationBlobWithRecovery(id []byte, data []byte) error {
	return service.putConversationBlobWithRecoveryMode(id, data, false)
}

func (service *Service) putProjectedConversationBlobWithRecovery(id []byte, data []byte) error {
	return service.putConversationBlobWithRecoveryMode(id, data, true)
}

func (service *Service) putConversationBlobWithRecoveryMode(id []byte, data []byte, projected bool) error {
	if service == nil || service.blobStore == nil {
		return fmt.Errorf("conversation blob store is not available")
	}
	put := service.blobStore.Put
	if projected {
		put = service.blobStore.PutProjected
	}
	err := put(id, data)
	if !errors.Is(err, errConversationBlobStoreFull) {
		return err
	}
	if pruneErr := service.pruneConversationBlobs(); pruneErr != nil {
		return fmt.Errorf("conversation blob store recovery failed: %v: %w", pruneErr, err)
	}
	return put(id, data)
}

func (service *Service) pruneConversationBlobs() error {
	if service == nil || service.store == nil || service.blobStore == nil || service.projector == nil {
		return nil
	}
	service.blobMaintenanceMu.Lock()
	defer service.blobMaintenanceMu.Unlock()
	conversationIDs, err := service.store.ListConversationIDs()
	if err != nil {
		return err
	}
	reachable := make(map[[sha256.Size]byte]struct{})
	for _, conversationID := range conversationIDs {
		conversation, err := service.store.LoadConversation(conversationID)
		if err != nil {
			return err
		}
		if conversation == nil {
			continue
		}
		projection, err := service.projector.ProjectLegacyCheckpointWithBlobs(conversation)
		if err != nil {
			return fmt.Errorf("project conversation %s for blob pruning: %w", conversationID, err)
		}
		for _, blob := range projection.Blobs {
			digest, err := parseBlobDigest(blob.ID)
			if err != nil {
				return err
			}
			reachable[digest] = struct{}{}
		}
	}
	return service.blobStore.Prune(reachable, time.Now().UTC().Add(-conversationBlobGCGracePeriod))
}

type pendingBlobMessage struct {
	ack  *pendingKVAck
	blob CheckpointBlob
}

type checkpointBlobSendError struct {
	err error
}

func (err *checkpointBlobSendError) Error() string {
	if err == nil || err.err == nil {
		return "send checkpoint blob"
	}
	return fmt.Sprintf("send checkpoint blob: %v", err.err)
}

func (err *checkpointBlobSendError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.err
}

func checkpointBlobIDs(blobs []CheckpointBlob) [][]byte {
	if len(blobs) == 0 {
		return nil
	}
	ids := make([][]byte, 0, len(blobs))
	for _, blob := range blobs {
		if len(blob.ID) == 0 {
			continue
		}
		ids = append(ids, append([]byte(nil), blob.ID...))
	}
	return ids
}

func (service *Service) deliverCheckpointBlobs(
	ctx context.Context,
	deadline time.Time,
	stream *ActiveStream,
	blobIDs [][]byte,
	delivered map[[sha256.Size]byte]struct{},
	send func(*agentv1.AgentServerMessage) error,
) (int, error) {
	if !deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(blobIDs) == 0 {
		return 0, nil
	}
	if service == nil || service.blobStore == nil || service.broker == nil {
		return 0, fmt.Errorf("conversation blob delivery is not available")
	}
	if send == nil {
		return 0, fmt.Errorf("conversation blob sender is not available")
	}
	if stream == nil {
		return 0, fmt.Errorf("request stream is not available")
	}
	blobs := make([]CheckpointBlob, 0, len(blobIDs))
	seen := make(map[[sha256.Size]byte]struct{}, len(blobIDs))
	for _, blobID := range blobIDs {
		if err := ctx.Err(); err != nil {
			return len(blobs), err
		}
		digest, err := parseBlobDigest(blobID)
		if err != nil {
			return len(blobs), err
		}
		if _, ok := delivered[digest]; ok {
			continue
		}
		if _, ok := seen[digest]; ok {
			continue
		}
		data, err := service.loadCheckpointBlob(stream, blobID)
		if err != nil {
			return len(blobs), fmt.Errorf("load checkpoint blob %s: %w", hex.EncodeToString(blobID), err)
		}
		seen[digest] = struct{}{}
		blobs = append(blobs, CheckpointBlob{ID: append([]byte(nil), blobID...), Data: data})
	}
	if len(blobs) == 0 {
		return 0, nil
	}

	pendingMessages := make([]pendingBlobMessage, 0, len(blobs))
	stream.mu.Lock()
	if stream.Status == StreamStatusCanceled || stream.Status == StreamStatusFailed {
		stream.mu.Unlock()
		return 0, fmt.Errorf("request is already terminal: %s", strings.TrimSpace(string(stream.Status)))
	}
	if stream.PendingKVAcks == nil {
		stream.PendingKVAcks = make(map[uint32]*pendingKVAck)
	}
	for _, blob := range blobs {
		digest, _ := parseBlobDigest(blob.ID)
		pending := service.newPendingKVAckLocked(stream, digest, pendingKVOperationSetBlob, nil)
		pendingMessages = append(pendingMessages, pendingBlobMessage{ack: pending, blob: blob})
	}
	stream.mu.Unlock()

	for _, item := range pendingMessages {
		if err := ctx.Err(); err != nil {
			service.cancelKVAcks(stream, pendingMessages, err)
			rememberDeliveredBlobAcks(pendingMessages, delivered)
			return len(pendingMessages), err
		}
		if err := send(buildSetBlobMessage(item.ack.id, item.blob)); err != nil {
			sendErr := &checkpointBlobSendError{err: err}
			service.cancelKVAcks(stream, pendingMessages, sendErr)
			rememberDeliveredBlobAcks(pendingMessages, delivered)
			return len(pendingMessages), sendErr
		}
	}
	acks := make([]*pendingKVAck, 0, len(pendingMessages))
	for _, item := range pendingMessages {
		acks = append(acks, item.ack)
	}
	if err := service.waitForKVAcks(ctx, acks); err != nil {
		service.cancelKVAcks(stream, pendingMessages, err)
		rememberDeliveredBlobAcks(pendingMessages, delivered)
		return len(pendingMessages), err
	}
	rememberDeliveredBlobAcks(pendingMessages, delivered)
	return len(pendingMessages), nil
}

func (service *Service) loadCheckpointBlob(stream *ActiveStream, blobID []byte) ([]byte, error) {
	data, err := service.blobStore.Get(blobID)
	if err == nil {
		return data, nil
	}
	if stream == nil || service.projector == nil {
		return nil, err
	}
	conversation, _, _, snapshotErr := service.snapshotCheckpointConversation(stream)
	if snapshotErr != nil {
		return nil, fmt.Errorf("repair checkpoint blob after %v: %w", err, snapshotErr)
	}
	projection, projectionErr := service.projector.ProjectLegacyCheckpointWithBlobs(conversation)
	if projectionErr != nil {
		return nil, fmt.Errorf("repair checkpoint blob after %v: %w", err, projectionErr)
	}
	for _, blob := range projection.Blobs {
		if !bytes.Equal(blob.ID, blobID) {
			continue
		}
		if putErr := service.putProjectedConversationBlobWithRecovery(blob.ID, blob.Data); putErr != nil {
			return nil, fmt.Errorf("repair checkpoint blob after %v: %w", err, putErr)
		}
		return service.blobStore.Get(blobID)
	}
	return nil, err
}

func (service *Service) cancelKVAcks(stream *ActiveStream, messages []pendingBlobMessage, cause error) {
	pending := make([]*pendingKVAck, 0, len(messages))
	for _, item := range messages {
		pending = append(pending, item.ack)
	}
	service.cancelPendingKVAcks(stream, pending, cause)
}

func (service *Service) cancelPendingKVAcks(stream *ActiveStream, pending []*pendingKVAck, cause error) {
	if stream == nil {
		for _, item := range pending {
			item.complete(cause)
		}
		return
	}
	stream.mu.Lock()
	for _, item := range pending {
		if item == nil {
			continue
		}
		if stream.PendingKVAcks[item.id] == item {
			delete(stream.PendingKVAcks, item.id)
		}
		item.complete(cause)
	}
	stream.mu.Unlock()
}

func (service *Service) acknowledgeKVClientMessage(requestID string, message *agentv1.KvClientMessage) (appendSequenceGeneration, bool) {
	if service == nil || message == nil {
		return appendSequenceGeneration{}, false
	}
	route, ok := service.lookupKVAckRoute(requestID, message.GetId())
	if !ok || !kvAckRouteMatchesMessage(route, message) {
		return appendSequenceGeneration{}, false
	}
	if route.completed {
		return route.appendGeneration, true
	}
	stream := route.stream
	if stream == nil {
		return appendSequenceGeneration{}, false
	}
	stream.mu.Lock()
	pending := stream.PendingKVAcks[message.GetId()]
	if pending == nil {
		stream.mu.Unlock()
		if completed, found := service.lookupKVAckRoute(requestID, message.GetId()); found && completed.completed {
			return completed.appendGeneration, true
		}
		return appendSequenceGeneration{}, false
	}
	if pending.kind == pendingKVOperationSetBlob {
		var resultErr error
		if result := message.GetSetBlobResult(); result == nil {
			resultErr = fmt.Errorf("client returned unexpected KV result for SetBlob")
		} else if resultError := result.GetError(); resultError != nil {
			resultErr = fmt.Errorf("client rejected blob: %s", strings.TrimSpace(resultError.GetMessage()))
		}
		delete(stream.PendingKVAcks, pending.id)
		stream.mu.Unlock()
		pending.complete(resultErr)
		return service.completedKVAckGeneration(requestID, pending.id)
	}
	stream.mu.Unlock()

	var resultErr error
	result := message.GetGetBlobResult()
	if pending.kind != pendingKVOperationGetBlob || result == nil {
		resultErr = fmt.Errorf("client returned unexpected KV result for GetBlob")
	} else {
		blobData := result.GetBlobData()
		if _, err := validatedBlobDigest(pending.digest[:], blobData); err != nil {
			resultErr = fmt.Errorf("validate client blob: %w", err)
		} else if err := service.putConversationBlobWithRecovery(pending.digest[:], blobData); err != nil {
			resultErr = fmt.Errorf("store client blob: %w", err)
		}
	}
	stream.mu.Lock()
	if stream.PendingKVAcks[pending.id] == pending {
		delete(stream.PendingKVAcks, pending.id)
	}
	stream.mu.Unlock()
	service.clearRequestedConversationBlobEvents(stream, []*pendingKVAck{pending})
	pending.complete(resultErr)
	return service.completedKVAckGeneration(requestID, pending.id)
}

func (service *Service) completedKVAckGeneration(requestID string, id uint32) (appendSequenceGeneration, bool) {
	route, ok := service.lookupKVAckRoute(requestID, id)
	if !ok || !route.completed {
		return appendSequenceGeneration{}, false
	}
	return route.appendGeneration, true
}

func (service *Service) lookupKVAckRoute(requestID string, id uint32) (kvAckRoute, bool) {
	if service == nil || id == 0 {
		return kvAckRoute{}, false
	}
	requestID = strings.TrimSpace(requestID)
	service.kvAckMu.Lock()
	route, ok := service.kvAckRoutes[id]
	if ok && route.createdAt.Before(time.Now().UTC().Add(-completedKVAckRetention)) {
		delete(service.kvAckRoutes, id)
		ok = false
	}
	service.kvAckMu.Unlock()
	if !ok || route.requestID != requestID {
		return kvAckRoute{}, false
	}
	return route, true
}

func (service *Service) hasKVAckRoute(id uint32) bool {
	if service == nil || id == 0 {
		return false
	}
	service.kvAckMu.Lock()
	defer service.kvAckMu.Unlock()
	route, ok := service.kvAckRoutes[id]
	if ok && route.createdAt.Before(time.Now().UTC().Add(-completedKVAckRetention)) {
		delete(service.kvAckRoutes, id)
		return false
	}
	return ok
}

func (service *Service) registerKVAckRoute(id uint32, stream *ActiveStream, kind pendingKVOperation) {
	if service == nil || id == 0 || stream == nil {
		return
	}
	now := time.Now().UTC()
	cutoff := now.Add(-completedKVAckRetention)
	service.kvAckMu.Lock()
	generation := service.appendSeq.CaptureGeneration(stream.RequestID)
	if service.kvAckRoutes == nil {
		service.kvAckRoutes = make(map[uint32]kvAckRoute)
	}
	for routeID, route := range service.kvAckRoutes {
		if route.createdAt.Before(cutoff) {
			delete(service.kvAckRoutes, routeID)
		}
	}
	route := kvAckRoute{
		requestID:        strings.TrimSpace(stream.RequestID),
		stream:           stream,
		streamInstanceID: stream.InstanceID,
		kind:             kind,
		appendGeneration: generation,
		createdAt:        now,
	}
	for _, existing := range service.kvAckRoutes {
		if sameKVAckRouteGeneration(existing, route) && existing.restartDone != nil {
			route.restartDone = existing.restartDone
			break
		}
	}
	service.kvAckRoutes[id] = route
	service.kvAckMu.Unlock()
}

func (service *Service) completeKVAckRoute(id uint32) {
	if service == nil || id == 0 {
		return
	}
	service.kvAckMu.Lock()
	route, ok := service.kvAckRoutes[id]
	if ok {
		route.stream = nil
		route.completed = true
		route.createdAt = time.Now().UTC()
		service.kvAckRoutes[id] = route
	}
	service.kvAckMu.Unlock()
}

func (service *Service) kvAckMessageMatches(requestID string, message *agentv1.KvClientMessage) bool {
	if service == nil || message == nil {
		return false
	}
	route, ok := service.lookupKVAckRoute(requestID, message.GetId())
	return ok && kvAckRouteMatchesMessage(route, message)
}

func (service *Service) trackKVAckAppendSequence(requestID string, message *agentv1.KvClientMessage, appendSeq int64, fingerprint [sha256.Size]byte) bool {
	if service == nil || message == nil || appendSeq <= 0 {
		return false
	}
	requestID = strings.TrimSpace(requestID)
	service.kvAckMu.Lock()
	defer service.kvAckMu.Unlock()
	route, ok := service.kvAckRoutes[message.GetId()]
	if !ok || route.requestID != requestID || route.createdAt.Before(time.Now().UTC().Add(-completedKVAckRetention)) || !kvAckRouteMatchesMessage(route, message) {
		return false
	}
	if route.appendSequences == nil {
		route.appendSequences = make(map[kvAckAppendSequenceKey]appendSequenceGeneration)
	}
	route.appendSequences[kvAckAppendSequenceKey{appendSeq: appendSeq, fingerprint: fingerprint}] = route.appendGeneration
	service.kvAckRoutes[message.GetId()] = route
	return true
}

func (service *Service) completeTrackedKVAckAppendSequence(requestID string, id uint32, appendSeq int64, fingerprint [sha256.Size]byte) (bool, appendSequenceCompletion) {
	if service == nil || appendSeq <= 0 {
		return false, appendSequenceCompletion{}
	}
	requestID = strings.TrimSpace(requestID)
	key := kvAckAppendSequenceKey{appendSeq: appendSeq, fingerprint: fingerprint}
	for attempt := 0; attempt < 3; attempt++ {
		service.kvAckMu.Lock()
		route, ok := service.kvAckRoutes[id]
		if !ok || route.requestID != requestID {
			service.kvAckMu.Unlock()
			return false, appendSequenceCompletion{}
		}
		generation := route.appendGeneration
		if observedGeneration, tracked := route.appendSequences[key]; tracked {
			generation = observedGeneration
		}
		service.kvAckMu.Unlock()
		matched, completion := service.completeAppendAheadForGeneration(requestID, appendSeq, generation, fingerprint)
		if matched {
			return true, completion
		}
	}
	return false, appendSequenceCompletion{}
}

func kvAckRouteMatchesMessage(route kvAckRoute, message *agentv1.KvClientMessage) bool {
	if message == nil {
		return false
	}
	switch route.kind {
	case pendingKVOperationSetBlob:
		return message.GetSetBlobResult() != nil
	case pendingKVOperationGetBlob:
		return message.GetGetBlobResult() != nil
	default:
		return false
	}
}

func sameKVAckRouteGeneration(left kvAckRoute, right kvAckRoute) bool {
	return left.requestID == right.requestID &&
		left.streamInstanceID == right.streamInstanceID &&
		left.appendGeneration == right.appendGeneration
}

func (service *Service) beginAppendSequenceReplayRestart(requestID string, appendSeq int64, fingerprint [sha256.Size]byte) kvAckRestartToken {
	if service == nil || service.appendSeq == nil {
		return kvAckRestartToken{}
	}
	generation, candidate := service.appendSeq.ReplayRestartCandidate(requestID, appendSeq, fingerprint)
	if !candidate {
		return kvAckRestartToken{}
	}
	return service.beginKVAckSequenceRestartForGeneration(requestID, generation)
}

func (service *Service) beginKVAckSequenceRestartForGeneration(requestID string, generation appendSequenceGeneration) kvAckRestartToken {
	if service == nil || generation.state == nil || service.broker == nil {
		return kvAckRestartToken{}
	}
	requestID = strings.TrimSpace(requestID)
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return kvAckRestartToken{}
	}
	streamInstanceID := stream.InstanceID
	service.kvAckMu.Lock()
	defer service.kvAckMu.Unlock()

	var token kvAckRestartToken
	for _, route := range service.kvAckRoutes {
		if route.requestID != requestID || route.streamInstanceID != streamInstanceID || route.appendGeneration != generation {
			continue
		}
		token = kvAckRestartToken{
			requestID:        requestID,
			streamInstanceID: streamInstanceID,
			appendGeneration: generation,
			done:             route.restartDone,
		}
		break
	}
	if token.appendGeneration.state == nil {
		return kvAckRestartToken{}
	}
	if token.done == nil {
		token.done = make(chan struct{})
	}
	for id, route := range service.kvAckRoutes {
		if route.requestID == token.requestID &&
			route.streamInstanceID == token.streamInstanceID &&
			route.appendGeneration == token.appendGeneration {
			route.restartDone = token.done
			service.kvAckRoutes[id] = route
		}
	}
	return token
}

func (service *Service) completeAppendAheadForGeneration(requestID string, appendSeq int64, generation appendSequenceGeneration, fingerprint [sha256.Size]byte) (bool, appendSequenceCompletion) {
	token := service.beginAppendSequenceReplayRestart(requestID, appendSeq, fingerprint)
	matched, completion := service.appendSeq.CompleteAheadForGenerationWithFingerprint(requestID, appendSeq, generation, &fingerprint)
	if !matched || completion.retry || !completion.replayRestarted {
		service.abortKVAckSequenceRestart(token)
		return matched, completion
	}
	service.adoptKVAckSequenceRestart(token, completion.generation)
	return matched, completion
}

func (service *Service) finishAppendSequenceReplayRestart(token kvAckRestartToken, ticket appendSequenceTicket) {
	if token.done != nil && token.appendGeneration != ticket.Generation() {
		service.adoptKVAckSequenceRestart(token, ticket.Generation())
		return
	}
	service.abortKVAckSequenceRestart(token)
}

func (service *Service) abortKVAckSequenceRestart(token kvAckRestartToken) {
	if service == nil || token.done == nil {
		return
	}
	service.kvAckMu.Lock()
	matched := false
	for id, route := range service.kvAckRoutes {
		if route.requestID != token.requestID ||
			route.streamInstanceID != token.streamInstanceID ||
			route.appendGeneration != token.appendGeneration ||
			route.restartDone != token.done {
			continue
		}
		route.restartDone = nil
		service.kvAckRoutes[id] = route
		matched = true
	}
	if matched {
		close(token.done)
	}
	service.kvAckMu.Unlock()
}

func (service *Service) beginKVAckSequenceRestart(requestID string, message *agentv1.KvClientMessage) kvAckRestartToken {
	if service == nil || message == nil {
		return kvAckRestartToken{}
	}
	requestID = strings.TrimSpace(requestID)
	now := time.Now().UTC()
	service.kvAckMu.Lock()
	defer service.kvAckMu.Unlock()
	route, ok := service.kvAckRoutes[message.GetId()]
	if !ok || route.requestID != requestID || route.createdAt.Before(now.Add(-completedKVAckRetention)) || !kvAckRouteMatchesMessage(route, message) {
		return kvAckRestartToken{}
	}
	token := kvAckRestartToken{
		requestID:        route.requestID,
		streamInstanceID: route.streamInstanceID,
		appendGeneration: route.appendGeneration,
		done:             route.restartDone,
	}
	if token.done != nil {
		return token
	}
	token.done = make(chan struct{})
	for id, candidate := range service.kvAckRoutes {
		if sameKVAckRouteGeneration(candidate, route) {
			candidate.restartDone = token.done
			service.kvAckRoutes[id] = candidate
		}
	}
	return token
}

func (service *Service) adoptKVAckSequenceRestart(token kvAckRestartToken, generation appendSequenceGeneration) bool {
	if service == nil || token.done == nil || generation.state == nil {
		return false
	}
	service.kvAckMu.Lock()
	matched := false
	for id, route := range service.kvAckRoutes {
		if route.requestID != token.requestID ||
			route.streamInstanceID != token.streamInstanceID ||
			route.appendGeneration != token.appendGeneration ||
			route.restartDone != token.done {
			continue
		}
		route.appendGeneration = generation
		route.restartDone = nil
		service.kvAckRoutes[id] = route
		matched = true
	}
	if matched {
		close(token.done)
	}
	service.kvAckMu.Unlock()
	return matched
}

func (service *Service) newPendingKVAckLocked(stream *ActiveStream, digest [sha256.Size]byte, kind pendingKVOperation, notify chan<- struct{}) *pendingKVAck {
	for {
		id := service.nextKVMessageID.Add(1)
		if id == 0 {
			continue
		}
		if _, exists := stream.PendingKVAcks[id]; exists {
			continue
		}
		if service.hasKVAckRoute(id) {
			continue
		}
		pending := &pendingKVAck{
			id:     id,
			digest: digest,
			kind:   kind,
			done:   make(chan struct{}),
			notify: notify,
		}
		pending.onComplete = func() {
			service.completeKVAckRoute(id)
		}
		stream.PendingKVAcks[id] = pending
		service.registerKVAckRoute(id, stream, kind)
		return pending
	}
}

func rememberDeliveredBlobAcks(messages []pendingBlobMessage, delivered map[[sha256.Size]byte]struct{}) {
	if delivered == nil {
		return
	}
	for _, item := range messages {
		if item.ack == nil {
			continue
		}
		select {
		case <-item.ack.done:
			if item.ack.result() == nil {
				delivered[item.ack.digest] = struct{}{}
			}
		default:
		}
	}
}

func (pending *pendingKVAck) complete(err error) {
	if pending == nil {
		return
	}
	pending.once.Do(func() {
		pending.err = err
		if pending.onComplete != nil {
			pending.onComplete()
		}
		close(pending.done)
		if pending.notify != nil {
			select {
			case pending.notify <- struct{}{}:
			default:
			}
		}
	})
}

func (pending *pendingKVAck) result() error {
	if pending == nil {
		return nil
	}
	<-pending.done
	return pending.err
}

func (service *Service) waitForKVAcks(ctx context.Context, acks []*pendingKVAck) error {
	if len(acks) == 0 {
		return nil
	}
	type ackResult struct {
		pending *pendingKVAck
		err     error
	}
	results := make(chan ackResult, len(acks))
	remaining := 0
	for _, pending := range acks {
		if pending == nil {
			continue
		}
		remaining++
		go func(item *pendingKVAck) {
			results <- ackResult{pending: item, err: item.result()}
		}(pending)
	}
	if remaining == 0 {
		return nil
	}
	idleTimer := time.NewTimer(conversationBlobAckTimeout)
	defer idleTimer.Stop()
	batchTimer := time.NewTimer(conversationBlobAckBatchTimeout)
	defer batchTimer.Stop()
	for remaining > 0 {
		select {
		case result := <-results:
			remaining--
			if result.err != nil {
				return fmt.Errorf("conversation blob acknowledgement %d failed: %w", result.pending.id, result.err)
			}
			if remaining > 0 {
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idleTimer.Reset(conversationBlobAckTimeout)
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-idleTimer.C:
			return fmt.Errorf("timed out waiting for conversation blob acknowledgements: %d remaining", remaining)
		case <-batchTimer.C:
			return fmt.Errorf("conversation blob acknowledgement batch exceeded %s: %d remaining", conversationBlobAckBatchTimeout, remaining)
		}
	}
	return nil
}

func buildSetBlobMessage(id uint32, blob CheckpointBlob) *agentv1.AgentServerMessage {
	return &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_KvServerMessage{
			KvServerMessage: &agentv1.KvServerMessage{
				Id: id,
				Message: &agentv1.KvServerMessage_SetBlobArgs{
					SetBlobArgs: &agentv1.SetBlobArgs{
						BlobId:   append([]byte(nil), blob.ID...),
						BlobData: append([]byte(nil), blob.Data...),
					},
				},
			},
		},
	}
}

func buildGetBlobMessage(id uint32, blobID []byte) *agentv1.AgentServerMessage {
	return &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_KvServerMessage{
			KvServerMessage: &agentv1.KvServerMessage{
				Id: id,
				Message: &agentv1.KvServerMessage_GetBlobArgs{
					GetBlobArgs: &agentv1.GetBlobArgs{BlobId: append([]byte(nil), blobID...)},
				},
			},
		},
	}
}

func checkpointBlobRetryDelay(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	shift := failures - 1
	if shift > 5 {
		shift = 5
	}
	delay := conversationBlobRetryBase * time.Duration(1<<shift)
	if delay > conversationBlobRetryMax {
		return conversationBlobRetryMax
	}
	return delay
}

func (service *Service) checkpointBlobDeliveryAborted(stream *ActiveStream) bool {
	if service == nil || stream == nil {
		return true
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.Status == StreamStatusCanceled || stream.Status == StreamStatusFailed
}

func (service *Service) waitForCheckpointBlobRetry(ctx context.Context, signal <-chan struct{}, stream *ActiveStream, retryAt time.Time) error {
	for {
		wait := time.Until(retryAt)
		if wait <= 0 || service.checkpointBlobDeliveryAborted(stream) {
			return nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-signal:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			return nil
		}
	}
}

type conversationBlobResolution struct {
	data    []byte
	missing bool
}

type conversationStateMaterialization struct {
	state           *agentv1.ConversationStateStructure
	missing         int
	missingIDs      map[[sha256.Size]byte]struct{}
	rootMissing     bool
	rootComplete    bool
	turnMissing     bool
	summaryMissing  bool
	requiredMissing bool
}

func (service *Service) resolveConversationBlob(reference []byte, field string) (conversationBlobResolution, error) {
	if len(reference) == 0 || len(reference) != sha256.Size {
		return conversationBlobResolution{data: append([]byte(nil), reference...)}, nil
	}
	if service == nil || service.blobStore == nil {
		return conversationBlobResolution{}, fmt.Errorf("resolve %s blob: conversation blob store is not available", field)
	}
	data, err := service.blobStore.Get(reference)
	if err == nil {
		return conversationBlobResolution{data: data}, nil
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, errConversationBlobCorrupt) {
		return conversationBlobResolution{data: append([]byte(nil), reference...), missing: true}, nil
	}
	return conversationBlobResolution{}, fmt.Errorf("resolve %s blob %s: %w", field, hex.EncodeToString(reference), err)
}

func (service *Service) materializeConversationState(ctx context.Context, stream *ActiveStream, state *agentv1.ConversationStateStructure) (*agentv1.ConversationStateStructure, error) {
	if state == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	idleTimer := time.NewTimer(conversationBlobImportIdleTime)
	defer idleTimer.Stop()
	poll := time.NewTicker(conversationBlobImportPoll)
	defer poll.Stop()
	progress := make(chan struct{}, 1)
	requested := make(map[[sha256.Size]byte]*pendingKVAck)
	defer func() {
		pending := make([]*pendingKVAck, 0, len(requested))
		for _, item := range requested {
			pending = append(pending, item)
		}
		service.clearRequestedConversationBlobEvents(stream, pending)
		service.cancelPendingKVAcks(stream, pending, context.Canceled)
	}()
	result, err := service.materializeConversationStateOnce(state)
	if err != nil {
		return nil, err
	}
	for {
		if result.missing == 0 {
			return result.state, nil
		}
		if err := service.requestMissingConversationBlobs(stream, result.missingIDs, requested, progress); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-progress:
			drainConversationBlobProgress(progress)
			resetConversationBlobIdleTimer(idleTimer)
			result, err = service.materializeConversationStateOnce(state)
			if err != nil {
				return nil, err
			}
		case <-poll.C:
			result, err = service.materializeConversationStateOnce(state)
			if err != nil {
				return nil, err
			}
		case <-idleTimer.C:
			select {
			case <-progress:
				drainConversationBlobProgress(progress)
				resetConversationBlobIdleTimer(idleTimer)
				result, err = service.materializeConversationStateOnce(state)
				if err != nil {
					return nil, err
				}
				continue
			default:
			}
			latest, err := service.materializeConversationStateOnce(state)
			if err != nil {
				return nil, err
			}
			return finalizeConversationStateMaterialization(latest)
		}
	}
}

func drainConversationBlobProgress(progress <-chan struct{}) {
	for {
		select {
		case <-progress:
		default:
			return
		}
	}
}

func resetConversationBlobIdleTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(conversationBlobImportIdleTime)
}

func (service *Service) clearRequestedConversationBlobEvents(stream *ActiveStream, pending []*pendingKVAck) {
	if stream == nil || len(pending) == 0 {
		return
	}
	ids := make(map[uint32]struct{}, len(pending))
	for _, item := range pending {
		if item != nil && item.kind == pendingKVOperationGetBlob {
			ids[item.id] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return
	}
	stream.mu.Lock()
	for index := len(stream.Backlog) - 1; index >= 0 && len(ids) > 0; index-- {
		message := stream.Backlog[index].Message
		kvMessage := message.GetKvServerMessage()
		if kvMessage == nil || kvMessage.GetGetBlobArgs() == nil {
			continue
		}
		if _, ok := ids[kvMessage.GetId()]; ok {
			stream.Backlog[index].Message = nil
			delete(ids, kvMessage.GetId())
		}
	}
	stream.mu.Unlock()
}

func (service *Service) requestMissingConversationBlobs(stream *ActiveStream, missing map[[sha256.Size]byte]struct{}, requested map[[sha256.Size]byte]*pendingKVAck, progress chan<- struct{}) error {
	if len(missing) == 0 || stream == nil {
		return nil
	}
	if service == nil || service.broker == nil {
		return fmt.Errorf("conversation blob retrieval is not available")
	}
	type request struct {
		digest  [sha256.Size]byte
		pending *pendingKVAck
	}
	inFlight := 0
	for _, pending := range requested {
		if pending == nil {
			continue
		}
		select {
		case <-pending.done:
		default:
			inFlight++
		}
	}
	available := conversationBlobGetMaxInFlight - inFlight
	if available <= 0 {
		return nil
	}
	digests := make([][sha256.Size]byte, 0, len(missing))
	for digest := range missing {
		if _, ok := requested[digest]; !ok {
			digests = append(digests, digest)
		}
	}
	sort.Slice(digests, func(i int, j int) bool {
		return bytes.Compare(digests[i][:], digests[j][:]) < 0
	})
	if len(digests) > available {
		digests = digests[:available]
	}
	requests := make([]request, 0, len(digests))
	stream.mu.Lock()
	if isTerminalStreamStatus(stream.Status) {
		stream.mu.Unlock()
		return context.Canceled
	}
	if stream.PendingKVAcks == nil {
		stream.PendingKVAcks = make(map[uint32]*pendingKVAck)
	}
	for _, digest := range digests {
		pending := service.newPendingKVAckLocked(stream, digest, pendingKVOperationGetBlob, progress)
		requested[digest] = pending
		requests = append(requests, request{digest: digest, pending: pending})
	}
	stream.mu.Unlock()
	for _, item := range requests {
		if err := service.broker.publishToStream(stream.RequestID, stream.InstanceID, StreamEvent{
			Message: buildGetBlobMessage(item.pending.id, item.digest[:]),
		}); err != nil {
			pending := make([]*pendingKVAck, 0, len(requests))
			for _, request := range requests {
				pending = append(pending, request.pending)
			}
			service.cancelPendingKVAcks(stream, pending, err)
			return fmt.Errorf("request conversation blob %s: %w", hex.EncodeToString(item.digest[:]), err)
		}
	}
	return nil
}

func (service *Service) materializeConversationStateOnce(state *agentv1.ConversationStateStructure) (conversationStateMaterialization, error) {
	materialized, ok := proto.Clone(state).(*agentv1.ConversationStateStructure)
	if !ok || materialized == nil {
		return conversationStateMaterialization{}, fmt.Errorf("clone conversation state")
	}
	result := conversationStateMaterialization{
		state:      materialized,
		missingIDs: make(map[[sha256.Size]byte]struct{}),
	}
	markMissing := func(reference []byte) {
		if digest, err := parseBlobDigest(reference); err == nil {
			result.missingIDs[digest] = struct{}{}
		}
	}
	resolveJSONList := func(items [][]byte, field string) ([][]byte, int, error) {
		result := make([][]byte, 0, len(items))
		missing := 0
		for index, item := range items {
			resolved, err := service.resolveConversationBlob(item, fmt.Sprintf("%s[%d]", field, index))
			if err != nil {
				return nil, 0, err
			}
			if len(resolved.data) == 0 {
				continue
			}
			if !json.Valid(resolved.data) {
				if resolved.missing {
					markMissing(item)
					missing++
					continue
				}
				return nil, 0, fmt.Errorf("decode materialized %s[%d] JSON", field, index)
			}
			result = append(result, resolved.data)
		}
		return result, missing, nil
	}
	resolveProtoList := func(items [][]byte, field string, newMessage func() proto.Message) ([][]byte, int, error) {
		result := make([][]byte, 0, len(items))
		missing := 0
		for index, item := range items {
			resolved, err := service.resolveConversationBlob(item, fmt.Sprintf("%s[%d]", field, index))
			if err != nil {
				return nil, 0, err
			}
			if len(resolved.data) == 0 {
				continue
			}
			if resolved.missing && !knownProtoPayload(resolved.data, newMessage()) {
				markMissing(item)
				missing++
				continue
			}
			result = append(result, resolved.data)
		}
		return result, missing, nil
	}
	resolveProtoValue := func(reference []byte, field string, newMessage func() proto.Message) ([]byte, bool, error) {
		resolved, err := service.resolveConversationBlob(reference, field)
		if err != nil {
			return nil, false, err
		}
		if resolved.missing && !knownProtoPayload(resolved.data, newMessage()) {
			return nil, true, nil
		}
		return resolved.data, false, nil
	}

	rootPromptMessages, missing, err := resolveJSONList(materialized.GetRootPromptMessagesJson(), "root_prompt_messages_json")
	if err != nil {
		return conversationStateMaterialization{}, err
	}
	materialized.RootPromptMessagesJson = rootPromptMessages
	result.missing += missing
	result.rootMissing = missing > 0
	result.rootComplete = missing == 0 && len(rootPromptMessages) > 0

	materialized.Todos, missing, err = resolveProtoList(materialized.GetTodos(), "todos", func() proto.Message { return &agentv1.TodoItem{} })
	if err != nil {
		return conversationStateMaterialization{}, err
	}
	result.missing += missing
	result.requiredMissing = result.requiredMissing || missing > 0
	materialized.SummaryArchives, missing, err = resolveProtoList(materialized.GetSummaryArchives(), "summary_archives", func() proto.Message { return &agentv1.ConversationSummaryArchive{} })
	if err != nil {
		return conversationStateMaterialization{}, err
	}
	result.missing += missing
	result.requiredMissing = result.requiredMissing || missing > 0
	summaryReference := materialized.GetSummary()
	materialized.Summary, result.summaryMissing, err = resolveProtoValue(summaryReference, "summary", func() proto.Message { return &agentv1.ConversationSummary{} })
	if err != nil {
		return conversationStateMaterialization{}, err
	}
	if result.summaryMissing {
		markMissing(summaryReference)
		result.missing++
	}
	var valueMissing bool
	summaryArchiveReference := materialized.GetSummaryArchive()
	materialized.SummaryArchive, valueMissing, err = resolveProtoValue(summaryArchiveReference, "summary_archive", func() proto.Message { return &agentv1.ConversationSummaryArchive{} })
	if err != nil {
		return conversationStateMaterialization{}, err
	}
	if valueMissing {
		markMissing(summaryArchiveReference)
		result.missing++
		result.requiredMissing = true
	}
	planReference := materialized.GetPlan()
	materialized.Plan, valueMissing, err = resolveProtoValue(planReference, "plan", func() proto.Message { return &agentv1.ConversationPlan{} })
	if err != nil {
		return conversationStateMaterialization{}, err
	}
	if valueMissing {
		markMissing(planReference)
		result.missing++
		result.requiredMissing = true
	}

	turns := make([][]byte, 0, len(materialized.GetTurns()))
	for index, turnReference := range materialized.GetTurns() {
		rawTurn, err := service.resolveConversationBlob(turnReference, fmt.Sprintf("turns[%d]", index))
		if err != nil {
			return conversationStateMaterialization{}, err
		}
		turn := &agentv1.ConversationTurnStructure{}
		if err := proto.Unmarshal(rawTurn.data, turn); err != nil || turn.GetTurn() == nil {
			if rawTurn.missing {
				markMissing(turnReference)
				result.missing++
				result.turnMissing = true
				continue
			}
			return conversationStateMaterialization{}, fmt.Errorf("decode materialized turn %d", index)
		}
		skipTurn := false
		if agentTurn := turn.GetAgentConversationTurn(); agentTurn != nil {
			userMessageReference := agentTurn.GetUserMessage()
			userMessage, err := service.resolveConversationBlob(userMessageReference, fmt.Sprintf("turns[%d].user_message", index))
			if err != nil {
				return conversationStateMaterialization{}, err
			}
			if userMessage.missing && !knownProtoPayload(userMessage.data, &agentv1.UserMessage{}) {
				markMissing(userMessageReference)
				result.missing++
				result.turnMissing = true
				skipTurn = true
			} else {
				agentTurn.UserMessage = userMessage.data
			}
			if !skipTurn {
				steps := make([][]byte, 0, len(agentTurn.GetSteps()))
				for stepIndex, stepReference := range agentTurn.GetSteps() {
					step, err := service.resolveConversationBlob(stepReference, fmt.Sprintf("turns[%d].steps[%d]", index, stepIndex))
					if err != nil {
						return conversationStateMaterialization{}, err
					}
					if step.missing && !knownProtoPayload(step.data, &agentv1.ConversationStep{}) {
						markMissing(stepReference)
						result.missing++
						result.turnMissing = true
						skipTurn = true
						break
					}
					steps = append(steps, step.data)
				}
				agentTurn.Steps = steps
			}
		}
		if skipTurn {
			continue
		}
		encoded, err := proto.Marshal(turn)
		if err != nil {
			return conversationStateMaterialization{}, fmt.Errorf("encode materialized turn %d: %w", index, err)
		}
		turns = append(turns, encoded)
	}
	materialized.Turns = turns
	return result, nil
}

func finalizeConversationStateMaterialization(result conversationStateMaterialization) (*agentv1.ConversationStateStructure, error) {
	if result.state == nil {
		return nil, fmt.Errorf("materialize conversation state")
	}
	if result.rootMissing {
		result.state.RootPromptMessagesJson = nil
	}
	if result.requiredMissing {
		return nil, fmt.Errorf("conversation state remains incomplete after %s without blob progress: %d blob(s) unavailable", conversationBlobImportIdleTime, result.missing)
	}
	if result.turnMissing && !result.rootComplete {
		return nil, fmt.Errorf("conversation history remains incomplete after %s without blob progress: %d blob(s) unavailable", conversationBlobImportIdleTime, result.missing)
	}
	if result.summaryMissing && !result.rootComplete {
		return nil, fmt.Errorf("conversation history remains incomplete after %s without blob progress: %d blob(s) unavailable", conversationBlobImportIdleTime, result.missing)
	}
	if result.rootMissing && !result.rootComplete && len(result.state.GetTurns()) == 0 {
		return nil, fmt.Errorf("conversation history remains incomplete after %s without blob progress: %d blob(s) unavailable", conversationBlobImportIdleTime, result.missing)
	}
	return result.state, nil
}

func knownProtoPayload(data []byte, message proto.Message) bool {
	if len(data) == 0 || message == nil {
		return false
	}
	if err := proto.Unmarshal(data, message); err != nil {
		return false
	}
	known := false
	message.ProtoReflect().Range(func(_ protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		known = true
		return false
	})
	return known
}
