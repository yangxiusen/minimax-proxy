package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/migrations"

	_ "modernc.org/sqlite"
)

type Options struct {
	ProtectedSlots int
	PerKeyLimit    int
	GlobalLimit    int
	Retention      time.Duration
	IdempotencyTTL time.Duration
	Now            func() time.Time
}

type Store struct {
	db      *sql.DB
	options Options
}

func Open(ctx context.Context, path string, options Options) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析数据库路径: %w", err)
	}
	dsnPath := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" {
		dsnPath = "/" + dsnPath
	}
	dsnURL := &url.URL{Scheme: "file", Path: dsnPath}
	query := dsnURL.Query()
	for _, pragma := range []string{"busy_timeout(5000)", "foreign_keys(1)", "journal_mode(WAL)", "synchronous(NORMAL)"} {
		query.Add("_pragma", pragma)
	}
	dsnURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, options: options}
	if store.options.Now == nil {
		store.options.Now = time.Now
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接 SQLite: %w", err)
	}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	return s.applyMigrations(ctx, []migration{
		{version: 1, name: "初始化", sql: migrations.Initial},
		{version: 2, name: "分辨率档位", sql: migrations.ResolutionTiers},
		{version: 3, name: "任务生命周期", sql: migrations.TaskLifecycleClosure},
		{version: 4, name: "模型节点配置", sql: migrations.ModelServiceNodes},
		{version: 5, name: "配置与阶段", sql: migrations.ProfilesAndStages},
		{version: 6, name: "产物生命周期", sql: migrations.ArtifactLifecycle},
		{version: 7, name: "节点单入口", sql: migrations.SingleEndpointNodes},
		{version: 8, name: "回调稳定负载", sql: migrations.CallbackDeliveryPayload},
		{version: 10, name: "对外 API Key", sql: migrations.ExternalAPIKeys},
		{version: 11, name: "请求配置简化", sql: migrations.RequestProfileSimplification},
		{version: 12, name: "对外 API Key 明文", sql: migrations.ExternalAPIKeyPlaintext},
		{version: 13, name: "动态逻辑分辨率", sql: migrations.DynamicRequestResolutions},
		{version: 14, name: "节点取消对账屏障", sql: migrations.NodeDispatchBarriers},
		{version: 15, name: "输入临时文件与后台维护", sql: migrations.InputSpoolAdminMaintenance},
		{version: 16, name: "MiniMax V2 与结果交付", sql: migrations.MiniMaxV2ResultDelivery},
		{version: 17, name: "官方 V2 Base64 输入", sql: migrations.OfficialV2Base64Inputs},
		{version: 18, name: "Base64 输入直传对象存储", sql: migrations.OSSDirectBase64Inputs},
		{version: 19, name: "官方提交基线状态", sql: migrations.OfficialSubmissionBaselineState},
		{version: 20, name: "上游反馈信息", sql: migrations.UpstreamFeedback},
		{version: 21, name: "对象存储输入元数据", sql: migrations.OSSInputObjectMetadata},
	})
}

type migration struct {
	version int
	name    string
	sql     string
}

func (s *Store) applyMigrations(ctx context.Context, plan []migration) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("获取数据库迁移连接: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("开始数据库迁移: %w", err)
	}
	defer func() {
		if err != nil {
			_, rollbackErr := conn.ExecContext(context.Background(), "ROLLBACK")
			err = errors.Join(err, rollbackErr)
		}
	}()
	for _, item := range plan {
		applied := 0
		if item.version > 1 {
			if queryErr := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=?`, item.version).Scan(&applied); queryErr != nil {
				return fmt.Errorf("查询数据库迁移版本 %d: %w", item.version, queryErr)
			}
		} else {
			if queryErr := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&applied); queryErr != nil {
				return fmt.Errorf("查询数据库迁移表: %w", queryErr)
			}
		}
		if applied != 0 {
			if item.version == 1 {
				if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(1,?)`, s.nowMillis()); err != nil {
					return fmt.Errorf("补记数据库迁移版本 1: %w", err)
				}
			}
			continue
		}
		if _, err := conn.ExecContext(ctx, item.sql); err != nil {
			return fmt.Errorf("执行数据库迁移 %d(%s): %w", item.version, item.name, err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, item.version, s.nowMillis()); err != nil {
			return fmt.Errorf("记录数据库迁移版本 %d: %w", item.version, err)
		}
	}
	if len(plan) > 0 {
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", plan[len(plan)-1].version)); err != nil {
			return fmt.Errorf("更新数据库用户版本: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("提交数据库迁移: %w", err)
	}
	return nil
}

func (s *Store) Create(ctx context.Context, input domain.NewTask, keyHash string, available func() bool) (taskResult domain.Task, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return domain.Task{}, err
	}
	defer completeTransaction(finish, &err)
	now := s.nowUnix()
	if keyHash != "" {
		var taskID, requestHash string
		err := conn.QueryRowContext(ctx, `SELECT i.task_id, i.request_hash FROM idempotency_keys i JOIN video_tasks t ON t.task_id=i.task_id WHERE i.api_key_id=? AND i.key_hash=? AND i.expires_at>? AND t.deleted_at IS NULL`, input.APIKeyID, keyHash, now).Scan(&taskID, &requestHash)
		if err == nil {
			if requestHash != input.RequestHash {
				return domain.Task{}, domain.ErrIdempotencyConflict
			}
			task, err := getWith(ctx, conn, input.APIKeyID, taskID, now)
			if err != nil {
				return domain.Task{}, err
			}
			return task, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return domain.Task{}, err
		}
	}
	if available != nil && !available() {
		return domain.Task{}, domain.ErrResourceUnavailable
	}
	if keyHash != "" {
		if _, err := conn.ExecContext(ctx, "DELETE FROM idempotency_keys WHERE api_key_id=? AND key_hash=?", input.APIKeyID, keyHash); err != nil {
			return domain.Task{}, err
		}
	}
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_tasks WHERE api_key_id=? AND deleted_at IS NULL AND status IN ('queued_open','queued_locked','dispatching','running','reconciling')`, input.APIKeyID).Scan(&count); err != nil {
		return domain.Task{}, err
	}
	if count >= s.options.PerKeyLimit {
		return domain.Task{}, domain.ErrPerKeyLimit
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_tasks WHERE deleted_at IS NULL AND status IN ('queued_open','queued_locked','dispatching','running','reconciling')`).Scan(&count); err != nil {
		return domain.Task{}, err
	}
	if count >= s.options.GlobalLimit {
		return domain.Task{}, domain.ErrGlobalLimit
	}
	expires := now + int64(s.options.Retention/time.Second)
	_, err = conn.ExecContext(ctx, `INSERT INTO video_tasks(task_id,api_key_id,model,scenario,request_json,request_hash,status,resolution,duration,ratio_requested,usage_input_image_count,callback_url_ciphertext,callback_url_nonce,profile_id,profile_version,config_snapshot_json,config_hash,created_at,updated_at,expires_at) VALUES(?,?,?,?,?,?,'queued_open',?,?,?,?,NULLIF(?,X''),NULLIF(?,X''),NULLIF(?,''),NULLIF(?,0),NULLIF(?,''),NULLIF(?,''),?,?,?)`, input.TaskID, input.APIKeyID, input.Model, input.Scenario, input.RequestJSON, input.RequestHash, input.Resolution, input.Duration, input.Ratio, input.InputImageCount, input.CallbackURLCiphertext, input.CallbackURLNonce, input.ProfileID, input.ProfileVersion, input.ConfigSnapshotJSON, input.ConfigHash, now, now, expires)
	if err != nil {
		return domain.Task{}, fmt.Errorf("插入任务: %w", err)
	}
	if len(input.CallbackURLCiphertext) > 0 {
		if input.CallbackDeliveryID == "" || input.CallbackRequestBody == "" || input.CallbackRequestBodyHash == "" {
			return domain.Task{}, errors.New("callback 投递快照不完整")
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO callback_deliveries(id,task_id,external_status,state_version,status,request_body_hash,request_body,created_at,updated_at) VALUES(?,?,'queued',1,'pending',?,?,?,?)`, input.CallbackDeliveryID, input.TaskID, input.CallbackRequestBodyHash, []byte(input.CallbackRequestBody), now*1000, now*1000)
		if err != nil {
			return domain.Task{}, fmt.Errorf("插入 callback 投递: %w", err)
		}
	}
	for _, stage := range input.Stages {
		if stage.ID == "" || stage.StageType == "" || stage.ConfigSnapshotJSON == "" || stage.MaxAttempts < 1 {
			return domain.Task{}, errors.New("任务阶段快照无效")
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO task_stages(id,task_id,stage_order,stage_type,required,max_attempts,config_snapshot_json,created_at,updated_at) VALUES(?,?,?,?,1,?,?,?,?)`, stage.ID, input.TaskID, stage.StageOrder, stage.StageType, stage.MaxAttempts, stage.ConfigSnapshotJSON, now*1000, now*1000)
		if err != nil {
			return domain.Task{}, fmt.Errorf("插入任务阶段: %w", err)
		}
	}
	for _, file := range input.InputSpoolFiles {
		if file.TaskID == "" {
			file.TaskID = input.TaskID
		}
		if file.SourceKind == "" {
			file.SourceKind = "data_uri"
		}
		if err := validateInputSpoolFile(file); err != nil {
			return domain.Task{}, err
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO task_input_spool_files(id,task_id,content_index,content_type,role,source_kind,declared_mime,detected_mime,media_type,extension,relative_path,object_url,size_bytes,sha256,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			file.ID, file.TaskID, file.ContentIndex, file.ContentType, file.Role, file.SourceKind, nullEmpty(file.DeclaredMIME), nullEmpty(file.DetectedMIME), file.MediaType, file.Extension, file.RelativePath, nullEmpty(file.ObjectURL), file.SizeBytes, file.SHA256, now*1000, now*1000)
		if err != nil {
			return domain.Task{}, fmt.Errorf("插入输入临时文件元数据: %w", err)
		}
	}
	if keyHash != "" {
		_, err = conn.ExecContext(ctx, `INSERT INTO idempotency_keys(api_key_id,key_hash,request_hash,task_id,created_at,expires_at) VALUES(?,?,?,?,?,?)`, input.APIKeyID, keyHash, input.RequestHash, input.TaskID, now, now+int64(s.options.IdempotencyTTL/time.Second))
		if err != nil {
			return domain.Task{}, err
		}
	}
	if err := s.rebalance(ctx, conn, now); err != nil {
		return domain.Task{}, err
	}
	task, err := getWith(ctx, conn, input.APIKeyID, input.TaskID, now)
	if err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

func (s *Store) Get(ctx context.Context, owner, taskID string) (domain.Task, error) {
	return getWith(ctx, s.db, owner, taskID, s.nowUnix())
}

func (s *Store) GetTaskForExecution(ctx context.Context, taskID string) (domain.Task, error) {
	task, err := scanTask(s.db.QueryRowContext(ctx, taskSelect+` WHERE task_id=? AND deleted_at IS NULL`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, domain.ErrTaskNotFound
	}
	return task, err
}

func (s *Store) ActiveForUpstream(ctx context.Context, upstreamID string) (domain.Task, error) {
	task, err := scanTask(s.db.QueryRowContext(ctx, taskSelect+` WHERE upstream_id=? AND status IN ('dispatching','running','reconciling','cancelling') AND deleted_at IS NULL LIMIT 1`, upstreamID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, domain.ErrTaskNotFound
	}
	return task, err
}

func (s *Store) List(ctx context.Context, owner string, filter domain.TaskFilter) ([]domain.Task, int, error) {
	conditions := []string{"api_key_id=?", "deleted_at IS NULL", "expires_at>?"}
	args := []any{owner, s.nowUnix()}
	if filter.Status != "" {
		statuses := internalStatuses(filter.Status)
		if len(statuses) == 0 {
			return []domain.Task{}, 0, nil
		}
		marks := make([]string, len(statuses))
		for i, status := range statuses {
			marks[i], args = "?", append(args, status)
		}
		conditions = append(conditions, "status IN ("+strings.Join(marks, ",")+")")
	}
	if len(filter.TaskIDs) > 0 {
		marks := make([]string, len(filter.TaskIDs))
		for i, taskID := range filter.TaskIDs {
			marks[i], args = "?", append(args, taskID)
		}
		conditions = append(conditions, "task_id IN ("+strings.Join(marks, ",")+")")
	}
	where := strings.Join(conditions, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM video_tasks WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if filter.PageNum <= 0 {
		filter.PageNum = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 20
	}
	queryArgs := append(append([]any{}, args...), filter.PageSize, (filter.PageNum-1)*filter.PageSize)
	rows, err := s.db.QueryContext(ctx, taskSelect+" WHERE "+where+" ORDER BY created_at DESC,queue_seq DESC LIMIT ? OFFSET ?", queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]domain.Task, 0)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, task)
	}
	return items, total, rows.Err()
}

func (s *Store) ListAdminTasks(ctx context.Context, filter domain.AdminTaskFilter) ([]domain.AdminTaskSummary, int, error) {
	conditions := []string{"deleted_at IS NULL", "expires_at>?"}
	args := []any{s.nowUnix()}
	if filter.Status != "" {
		statuses := internalStatuses(filter.Status)
		if len(statuses) == 0 {
			return []domain.AdminTaskSummary{}, 0, nil
		}
		marks := make([]string, len(statuses))
		for i, status := range statuses {
			marks[i], args = "?", append(args, status)
		}
		conditions = append(conditions, "status IN ("+strings.Join(marks, ",")+")")
	}
	if filter.UpstreamID != "" {
		conditions = append(conditions, "upstream_id=?")
		args = append(args, filter.UpstreamID)
	}
	if filter.Search != "" {
		conditions = append(conditions, "(instr(task_id, ?)=1 OR instr(api_key_id, ?)=1)")
		args = append(args, filter.Search, filter.Search)
	}
	where := strings.Join(conditions, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM video_tasks WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if filter.PageNum <= 0 {
		filter.PageNum = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 10
	}
	pageNum, pageSize := int64(filter.PageNum), int64(filter.PageSize)
	const maxInt64 = int64(^uint64(0) >> 1)
	if pageNum-1 > maxInt64/pageSize {
		return []domain.AdminTaskSummary{}, total, nil
	}
	queryArgs := append(append([]any{}, args...), pageSize, (pageNum-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, adminTaskSelect+" WHERE "+where+" ORDER BY created_at DESC,queue_seq DESC LIMIT ? OFFSET ?", queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]domain.AdminTaskSummary, 0)
	for rows.Next() {
		item, err := scanAdminTaskSummary(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (s *Store) LatestFinishedForUpstream(ctx context.Context, upstreamID string) (domain.AdminTaskSummary, error) {
	item, err := scanAdminTaskSummary(s.db.QueryRowContext(ctx, adminTaskSelect+` WHERE upstream_id=? AND status IN ('succeeded','failed','cancelled') AND deleted_at IS NULL AND expires_at>? ORDER BY finished_at DESC,queue_seq DESC LIMIT 1`, upstreamID, s.nowUnix()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AdminTaskSummary{}, domain.ErrTaskNotFound
	}
	return item, err
}

func (s *Store) ClaimNext(ctx context.Context, upstreamID string, expectedVersion ...int64) (taskResult domain.Task, err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return domain.Task{}, err
	}
	defer completeTransaction(finish, &err)
	if len(expectedVersion) > 0 {
		var current int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_service_nodes WHERE id=? AND version=? AND enabled=1 AND deleted_at IS NULL`, upstreamID, expectedVersion[0]).Scan(&current); err != nil {
			return domain.Task{}, err
		}
		if current != 1 {
			return domain.Task{}, domain.ErrNodeConfigStale
		}
	}
	var active int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM video_tasks WHERE upstream_id=? AND status IN ('dispatching','running','reconciling','cancelling')`, upstreamID).Scan(&active); err != nil {
		return domain.Task{}, err
	}
	if active > 0 {
		return domain.Task{}, domain.ErrUpstreamBusy
	}
	wanted := domain.StatusQueuedOpen
	if s.options.ProtectedSlots > 0 {
		wanted = domain.StatusQueuedLocked
	}
	var taskID, owner string
	err = conn.QueryRowContext(ctx, `
		SELECT task.task_id,task.api_key_id
		FROM video_tasks task
		WHERE task.status=? AND task.deleted_at IS NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM json_each(task.request_json,'$.content') content
		    WHERE json_extract(content.value,'$.type') IN ('image_url','video_url','audio_url')
		      AND substr(lower(COALESCE(
		        json_extract(content.value,'$.image_url.url'),
		        json_extract(content.value,'$.video_url.url'),
		        json_extract(content.value,'$.audio_url.url'),
		        ''
		      )),1,10) = 'mm_file://'
		  )
		ORDER BY task.queue_seq LIMIT 1`, wanted).Scan(&taskID, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, domain.ErrQueueEmpty
	}
	if err != nil {
		return domain.Task{}, err
	}
	now := s.nowUnix()
	result, err := conn.ExecContext(ctx, `UPDATE video_tasks SET status='dispatching',cancel_locked=1,upstream_id=?,started_at=COALESCE(started_at,?),updated_at=?,version=version+1 WHERE task_id=? AND status=?`, upstreamID, now, now, taskID, wanted)
	if err != nil {
		return domain.Task{}, err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return domain.Task{}, domain.ErrStateConflict
	}
	if err := s.rebalance(ctx, conn, now); err != nil {
		return domain.Task{}, err
	}
	task, err := getWith(ctx, conn, owner, taskID, now)
	if err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

func (s *Store) CancelOrDelete(ctx context.Context, owner, taskID string) (actionResult domain.Action, err error) {
	var currentStatus domain.InternalStatus
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM video_tasks WHERE task_id=? AND api_key_id=? AND deleted_at IS NULL`, taskID, owner).Scan(&currentStatus); err == nil && currentStatus.AdminCanDelete() {
		if _, err := s.RequestOwnedTaskDeletion(ctx, owner, taskID); err != nil {
			if errors.Is(err, ErrDeletionNotFound) {
				return "", domain.ErrTaskNotFound
			}
			return "", err
		}
		return domain.ActionDeleted, nil
	}
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return "", err
	}
	defer completeTransaction(finish, &err)
	now := s.nowUnix()
	var status domain.InternalStatus
	err = conn.QueryRowContext(ctx, `SELECT status FROM video_tasks WHERE task_id=? AND api_key_id=? AND deleted_at IS NULL AND expires_at>?`, taskID, owner, now).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.ErrTaskNotFound
	}
	if err != nil {
		return "", err
	}
	var action domain.Action
	switch status {
	case domain.StatusQueuedOpen:
		result, err := conn.ExecContext(ctx, `UPDATE video_tasks SET status='cancelled',cancel_locked=0,finished_at=?,updated_at=?,version=version+1 WHERE task_id=? AND status='queued_open'`, now, now, taskID)
		if err != nil {
			return "", err
		}
		if rows, _ := result.RowsAffected(); rows != 1 {
			return "", domain.ErrStateConflict
		}
		if err := s.rebalance(ctx, conn, now); err != nil {
			return "", err
		}
		if err := createCallbackDeliveryWithConn(ctx, conn, taskID, "cancelled", now*1000); err != nil {
			return "", err
		}
		action = domain.ActionCancelled
	default:
		return "", domain.ErrTaskNotOperable
	}
	return action, nil
}

func (s *Store) MarkSucceeded(ctx context.Context, taskID, upstreamID, internalURL, publicURL, ratio string) error {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	now := s.nowUnix()
	result, err := conn.ExecContext(ctx, `UPDATE video_tasks SET status='succeeded',result_internal_url=?,result_public_url=?,ratio_actual=?,usage_total_seconds=duration,usage_output_seconds=duration,finished_at=?,updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND status IN ('dispatching','running','reconciling')`, internalURL, publicURL, ratio, now, now, taskID, upstreamID)
	if err := oneRow(result, err); err != nil {
		return err
	}
	return createCallbackDeliveryWithConn(ctx, conn, taskID, "succeeded", now*1000)
}

func (s *Store) SaveBaseline(ctx context.Context, taskID, upstreamID string, urls []string) error {
	data, err := json.Marshal(urls)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE video_tasks SET gallery_before_json=?,updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND status='dispatching'`, string(data), s.nowUnix(), taskID, upstreamID)
	return oneRow(result, err)
}

func (s *Store) SaveSubmissionContext(ctx context.Context, taskID, upstreamID string, jobIDs []string) error {
	data, err := json.Marshal(jobIDs)
	if err != nil {
		return err
	}
	now := s.nowUnix()
	result, err := s.db.ExecContext(ctx, `UPDATE video_tasks SET upstream_jobs_before_json=?,attempt_started_at=?,updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND status='dispatching'`, string(data), now, now, taskID, upstreamID)
	return oneRow(result, err)
}

func (s *Store) BindUpstreamJob(ctx context.Context, taskID, upstreamID, jobID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE video_tasks SET upstream_job_id=?,status='running',updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND status IN ('dispatching','reconciling')`, jobID, s.nowUnix(), taskID, upstreamID)
	return oneRow(result, err)
}

func (s *Store) BeginRetry(ctx context.Context, taskID, upstreamID string, jobIDsBefore, galleryBefore []string) error {
	jobsData, err := json.Marshal(jobIDsBefore)
	if err != nil {
		return err
	}
	galleryData, err := json.Marshal(galleryBefore)
	if err != nil {
		return err
	}
	now := s.nowUnix()
	result, err := s.db.ExecContext(ctx, `UPDATE video_tasks SET status='dispatching',retry_count=1,upstream_job_id=NULL,upstream_jobs_before_json=?,gallery_before_json=?,attempt_started_at=?,error_code=NULL,error_message=NULL,updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND retry_count=0 AND status IN ('running','reconciling')`, string(jobsData), string(galleryData), now, now, taskID, upstreamID)
	return oneRow(result, err)
}

func (s *Store) RequestAdminCancel(ctx context.Context, taskID string) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	now := s.nowUnix()
	var status domain.InternalStatus
	var upstreamID, activeStageID string
	err = conn.QueryRowContext(ctx, `SELECT status,COALESCE(upstream_id,''),COALESCE(active_stage_id,'') FROM video_tasks WHERE task_id=? AND deleted_at IS NULL AND expires_at>?`, taskID, now).Scan(&status, &upstreamID, &activeStageID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrTaskNotFound
	}
	if err != nil {
		return err
	}
	if upstreamID != "" && (status == domain.StatusDispatching || status == domain.StatusRunning || status == domain.StatusReconciling) {
		var protocol string
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(protocol_version,'') FROM model_service_nodes WHERE id=?`, upstreamID).Scan(&protocol); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if protocol == "minimax-v2" {
			return domain.ErrOfficialRunningNotCancellable
		}
	}
	hasBarrier, err := createNodeDispatchBarrierForTask(ctx, conn, taskID, now*1000)
	if err != nil {
		return err
	}
	switch status {
	case domain.StatusQueuedOpen, domain.StatusQueuedLocked:
		if hasBarrier {
			return finishStageTaskCancellation(ctx, conn, taskID, now)
		}
		result, execErr := conn.ExecContext(ctx, `UPDATE video_tasks SET status='cancelled',cancel_locked=0,cancel_requested_at=?,finished_at=?,updated_at=?,version=version+1 WHERE task_id=? AND status=?`, now, now, now, taskID, status)
		if err := oneRow(result, execErr); err != nil {
			return err
		}
		if err := s.rebalance(ctx, conn, now); err != nil {
			return err
		}
		return createCallbackDeliveryWithConn(ctx, conn, taskID, "cancelled", now*1000)
	case domain.StatusDispatching, domain.StatusRunning, domain.StatusReconciling:
		if hasBarrier || activeStageID != "" || upstreamID == "" {
			return finishStageTaskCancellation(ctx, conn, taskID, now)
		}
		result, execErr := conn.ExecContext(ctx, `UPDATE video_tasks SET status='cancelling',cancel_requested_at=?,updated_at=?,version=version+1 WHERE task_id=? AND status=?`, now, now, taskID, status)
		return oneRow(result, execErr)
	case domain.StatusCancelling:
		if hasBarrier || activeStageID != "" || upstreamID == "" {
			return finishStageTaskCancellation(ctx, conn, taskID, now)
		}
		if upstreamID != "" {
			return nil
		}
	default:
		return domain.ErrTaskNotOperable
	}
	return domain.ErrTaskNotOperable
}

func finishStageTaskCancellation(ctx context.Context, conn *sql.Conn, taskID string, now int64) error {
	if _, err := conn.ExecContext(ctx, `UPDATE stage_attempts SET status='failed',error_code='task_cancelled',error_message='任务已取消',heartbeat_at=?,finished_at=? WHERE stage_id IN (SELECT id FROM task_stages WHERE task_id=?) AND status IN ('dispatching','running','validating','unknown')`, now*1000, now*1000, taskID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE task_stages SET status='cancelled',lease_token=NULL,lease_expires_at=NULL,next_attempt_at=NULL,updated_at=?,finished_at=COALESCE(finished_at,?),row_version=row_version+1 WHERE task_id=? AND status NOT IN ('succeeded','failed','skipped','cancelled')`, now*1000, now*1000, taskID); err != nil {
		return err
	}
	result, err := conn.ExecContext(ctx, `UPDATE video_tasks SET status='cancelled',cancel_locked=0,cancel_requested_at=COALESCE(cancel_requested_at,?),upstream_id=NULL,active_stage_id=NULL,finished_at=?,updated_at=?,version=version+1 WHERE task_id=? AND status IN ('queued_open','queued_locked','dispatching','running','reconciling','cancelling')`, now, now, now, taskID)
	if err := oneRow(result, err); err != nil {
		return err
	}
	if err := createCallbackDeliveryWithConn(ctx, conn, taskID, "cancelled", now*1000); err != nil {
		return err
	}
	return nil
}

func (s *Store) FinishCancelled(ctx context.Context, taskID, upstreamID string) error {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	now := s.nowUnix()
	result, err := conn.ExecContext(ctx, `UPDATE video_tasks SET status='cancelled',cancel_locked=0,finished_at=?,updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND status='cancelling'`, now, now, taskID, upstreamID)
	if err := oneRow(result, err); err != nil {
		return err
	}
	return createCallbackDeliveryWithConn(ctx, conn, taskID, "cancelled", now*1000)
}

func (s *Store) AdminDelete(ctx context.Context, taskID string, purgedLocations ...domain.TaskArtifactLocation) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	var status domain.InternalStatus
	var version int64
	err = conn.QueryRowContext(ctx, `SELECT status,version FROM video_tasks WHERE task_id=? AND deleted_at IS NULL`, taskID).Scan(&status, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrTaskNotFound
	}
	if err != nil {
		return err
	}
	if !status.AdminCanDelete() {
		return domain.ErrTaskNotOperable
	}
	var barrierCount int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_dispatch_barriers WHERE task_id=?`, taskID).Scan(&barrierCount); err != nil {
		return err
	}
	if barrierCount != 0 {
		return domain.ErrCancelReconcilePending
	}
	var profileRefs int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM profile_test_runs WHERE artifact_id IN (SELECT id FROM task_artifacts WHERE task_id=?)`, taskID).Scan(&profileRefs); err != nil {
		return err
	}
	if profileRefs != 0 {
		return domain.ErrStateConflict
	}
	if purgedLocations != nil {
		currentLocations, err := listTaskArtifactLocationsWithConn(ctx, conn, taskID)
		if err != nil {
			return err
		}
		if !sameTaskArtifactLocations(currentLocations, purgedLocations) {
			return domain.ErrStateConflict
		}
	}
	if _, err := conn.ExecContext(ctx, `UPDATE video_tasks SET active_stage_id=NULL,result_artifact_id=NULL WHERE task_id=? AND version=?`, taskID, version); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE task_stages SET input_artifact_id=NULL,output_artifact_id=NULL WHERE task_id=?`, taskID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE task_id=?`, taskID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM callback_deliveries WHERE task_id=?`, taskID); err != nil {
		return err
	}
	rows, err := conn.QueryContext(ctx, `SELECT DISTINCT i.job_id FROM artifact_deletion_items i WHERE i.artifact_id IN (SELECT id FROM task_artifacts WHERE task_id=?) OR i.location_id IN (SELECT l.id FROM artifact_locations l JOIN task_artifacts a ON a.id=l.artifact_id WHERE a.task_id=?)`, taskID, taskID)
	if err != nil {
		return err
	}
	var deletionJobIDs []string
	for rows.Next() {
		var jobID string
		if err := rows.Scan(&jobID); err != nil {
			_ = rows.Close()
			return err
		}
		deletionJobIDs = append(deletionJobIDs, jobID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM artifact_deletion_items WHERE artifact_id IN (SELECT id FROM task_artifacts WHERE task_id=?) OR location_id IN (SELECT l.id FROM artifact_locations l JOIN task_artifacts a ON a.id=l.artifact_id WHERE a.task_id=?)`, taskID, taskID); err != nil {
		return err
	}
	for _, jobID := range deletionJobIDs {
		if _, err := conn.ExecContext(ctx, `DELETE FROM artifact_deletion_jobs WHERE id=? AND NOT EXISTS (SELECT 1 FROM artifact_deletion_items WHERE job_id=?)`, jobID, jobID); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM stage_attempts WHERE stage_id IN (SELECT id FROM task_stages WHERE task_id=?)`, taskID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM artifact_locations WHERE artifact_id IN (SELECT id FROM task_artifacts WHERE task_id=?)`, taskID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM task_artifacts WHERE task_id=?`, taskID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM task_stages WHERE task_id=?`, taskID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM task_input_spool_files WHERE task_id=?`, taskID); err != nil {
		return err
	}
	result, err := conn.ExecContext(ctx, `DELETE FROM video_tasks WHERE task_id=? AND status=? AND version=?`, taskID, status, version)
	return oneRow(result, err)
}

func (s *Store) MarkRunning(ctx context.Context, taskID, upstreamID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE video_tasks SET status='running',updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND status='dispatching'`, s.nowUnix(), taskID, upstreamID)
	return oneRow(result, err)
}

func (s *Store) MarkReconciling(ctx context.Context, taskID, upstreamID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE video_tasks SET status='reconciling',updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND status IN ('dispatching','running','reconciling')`, s.nowUnix(), taskID, upstreamID)
	return oneRow(result, err)
}

func (s *Store) MarkFailed(ctx context.Context, taskID, upstreamID, code, message string) error {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	now := s.nowUnix()
	result, err := conn.ExecContext(ctx, `UPDATE video_tasks SET status='failed',error_code=?,error_message=?,finished_at=?,updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND status IN ('dispatching','running','reconciling')`, code, message, now, now, taskID, upstreamID)
	if err := oneRow(result, err); err != nil {
		return err
	}
	return createCallbackDeliveryWithConn(ctx, conn, taskID, "failed", now*1000)
}

func (s *Store) Requeue(ctx context.Context, taskID, upstreamID string) (err error) {
	conn, finish, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer completeTransaction(finish, &err)
	result, err := conn.ExecContext(ctx, `UPDATE video_tasks SET status='queued_open',cancel_locked=0,upstream_id=NULL,gradio_event_id=NULL,upstream_job_id=NULL,upstream_jobs_before_json='[]',official_submission_baseline_saved=0,upstream_feedback_json=NULL,retry_count=0,attempt_started_at=NULL,cancel_requested_at=NULL,gallery_before_json=NULL,started_at=NULL,updated_at=?,version=version+1 WHERE task_id=? AND upstream_id=? AND status='dispatching'`, s.nowUnix(), taskID, upstreamID)
	if err := oneRow(result, err); err != nil {
		return err
	}
	if err := s.rebalance(ctx, conn, s.nowUnix()); err != nil {
		return err
	}
	return nil
}

func (s *Store) CleanupExpired(ctx context.Context, batchSize int) (taskCount int, keyCount int, err error) {
	if batchSize <= 0 {
		batchSize = 100
	}
	now := s.nowUnix()
	rows, err := s.db.QueryContext(ctx, `SELECT task_id FROM video_tasks WHERE deleted_at IS NULL AND expires_at<=? AND status IN ('succeeded','failed','cancelled') ORDER BY queue_seq LIMIT ?`, now, batchSize)
	if err != nil {
		return 0, 0, err
	}
	var taskIDs []string
	for rows.Next() {
		var taskID string
		if scanErr := rows.Scan(&taskID); scanErr != nil {
			rows.Close()
			return 0, 0, scanErr
		}
		taskIDs = append(taskIDs, taskID)
	}
	if closeErr := rows.Close(); closeErr != nil {
		return 0, 0, closeErr
	}
	for _, taskID := range taskIDs {
		if _, requestErr := s.RequestTaskDeletion(ctx, taskID, "retention", "retention-worker"); requestErr != nil {
			return taskCount, 0, requestErr
		}
		taskCount++
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE id IN (SELECT id FROM idempotency_keys WHERE expires_at<=? ORDER BY id LIMIT ?)`, now, batchSize)
	if err != nil {
		return taskCount, 0, err
	}
	keyRows, err := result.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	return taskCount, int(keyRows), nil
}

func oneRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return domain.ErrStateConflict
	}
	return nil
}

func (s *Store) rebalance(ctx context.Context, conn *sql.Conn, now int64) error {
	_, err := conn.ExecContext(ctx, `WITH ranked AS (SELECT queue_seq,ROW_NUMBER() OVER (ORDER BY queue_seq) rn FROM video_tasks WHERE deleted_at IS NULL AND status IN ('queued_open','queued_locked')) UPDATE video_tasks SET status=CASE WHEN queue_seq IN (SELECT queue_seq FROM ranked WHERE rn<=?) THEN 'queued_locked' ELSE 'queued_open' END,cancel_locked=CASE WHEN queue_seq IN (SELECT queue_seq FROM ranked WHERE rn<=?) THEN 1 ELSE 0 END,updated_at=?,version=version+1 WHERE queue_seq IN (SELECT queue_seq FROM ranked)`, s.options.ProtectedSlots, s.options.ProtectedSlots, now)
	return err
}

func (s *Store) immediate(ctx context.Context) (*sql.Conn, func(bool) error, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	finish := func(commit bool) error {
		var transactionErr error
		if commit {
			_, transactionErr = conn.ExecContext(context.Background(), "COMMIT")
		} else {
			_, transactionErr = conn.ExecContext(context.Background(), "ROLLBACK")
		}
		closeErr := conn.Close()
		return errors.Join(transactionErr, closeErr)
	}
	return conn, finish, nil
}

func completeTransaction(finish func(bool) error, operationErr *error) {
	finishErr := finish(*operationErr == nil)
	if *operationErr == nil && finishErr != nil {
		*operationErr = fmt.Errorf("提交 SQLite 事务: %w", finishErr)
	}
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type rowScanner interface{ Scan(...any) error }

const taskSelect = `SELECT queue_seq,task_id,api_key_id,model,scenario,request_json,request_hash,status,cancel_locked,COALESCE(upstream_id,''),COALESCE(gradio_event_id,''),COALESCE(upstream_job_id,''),upstream_slot_active,COALESCE(upstream_node_version,0),delivery_required,upstream_jobs_before_json,official_submission_baseline_saved,retry_count,COALESCE(attempt_started_at,0),COALESCE(cancel_requested_at,0),COALESCE(gallery_before_json,''),COALESCE(result_internal_url,''),COALESCE(result_public_url,''),resolution,duration,ratio_requested,COALESCE(ratio_actual,''),usage_total_seconds,usage_input_seconds,usage_output_seconds,usage_input_image_count,COALESCE(error_code,''),COALESCE(error_message,''),COALESCE(upstream_feedback_json,''),created_at,updated_at,COALESCE(started_at,0),COALESCE(finished_at,0),expires_at,version,COALESCE(profile_id,''),COALESCE(profile_version,0),COALESCE(config_snapshot_json,''),COALESCE(config_hash,''),COALESCE(active_stage_id,''),COALESCE(result_artifact_id,'') FROM video_tasks`

const adminTaskSelect = `SELECT task_id,api_key_id,COALESCE(upstream_id,''),COALESCE((SELECT protocol_version FROM model_service_nodes WHERE id=video_tasks.upstream_id),''),scenario,resolution,status,retry_count,COALESCE(result_public_url,''),COALESCE(result_artifact_id,''),duration,created_at,COALESCE(started_at,0),COALESCE(finished_at,0) FROM video_tasks`

func getWith(ctx context.Context, query rowQuerier, owner, taskID string, now int64) (domain.Task, error) {
	statement := taskSelect + ` WHERE task_id=? AND api_key_id=? AND deleted_at IS NULL AND expires_at>?`
	task, err := scanTask(query.QueryRowContext(ctx, statement, taskID, owner, now))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, domain.ErrTaskNotFound
	}
	return task, err
}

func scanTask(scanner rowScanner) (domain.Task, error) {
	var task domain.Task
	var upstreamFeedbackJSON string
	var locked, upstreamSlotActive, deliveryRequired, officialSubmissionBaselineSaved int
	var created, updated, started, finished, expires, attemptStarted, cancelRequested int64
	err := scanner.Scan(&task.QueueSeq, &task.TaskID, &task.APIKeyID, &task.Model, &task.Scenario, &task.RequestJSON, &task.RequestHash, &task.Status, &locked, &task.UpstreamID, &task.GradioEventID, &task.UpstreamJobID, &upstreamSlotActive, &task.UpstreamNodeVersion, &deliveryRequired, &task.UpstreamJobsBeforeJSON, &officialSubmissionBaselineSaved, &task.RetryCount, &attemptStarted, &cancelRequested, &task.GalleryBeforeJSON, &task.ResultInternalURL, &task.ResultPublicURL, &task.Resolution, &task.Duration, &task.RatioRequested, &task.RatioActual, &task.UsageTotalSeconds, &task.UsageInputSeconds, &task.UsageOutputSeconds, &task.UsageInputImageCount, &task.ErrorCode, &task.ErrorMessage, &upstreamFeedbackJSON, &created, &updated, &started, &finished, &expires, &task.Version, &task.ProfileID, &task.ProfileVersion, &task.ConfigSnapshotJSON, &task.ConfigHash, &task.ActiveStageID, &task.ResultArtifactID)
	if err != nil {
		return domain.Task{}, err
	}
	task.CancelLocked = locked == 1
	task.UpstreamSlotActive = upstreamSlotActive == 1
	task.DeliveryRequired = deliveryRequired == 1
	task.OfficialSubmissionBaselineSaved = officialSubmissionBaselineSaved == 1
	if upstreamFeedbackJSON != "" {
		task.UpstreamFeedback = &domain.UpstreamFeedback{}
		if err := json.Unmarshal([]byte(upstreamFeedbackJSON), task.UpstreamFeedback); err != nil {
			return domain.Task{}, fmt.Errorf("解析上游反馈: %w", err)
		}
	}
	task.CreatedAt, task.UpdatedAt, task.ExpiresAt = unix(created), unix(updated), unix(expires)
	if started > 0 {
		task.StartedAt = unix(started)
	}
	if finished > 0 {
		task.FinishedAt = unix(finished)
	}
	if attemptStarted > 0 {
		task.AttemptStartedAt = unix(attemptStarted)
	}
	if cancelRequested > 0 {
		task.CancelRequestedAt = unix(cancelRequested)
	}
	return task, nil
}

func scanAdminTaskSummary(scanner rowScanner) (domain.AdminTaskSummary, error) {
	var item domain.AdminTaskSummary
	var status domain.InternalStatus
	var created, started, finished int64
	if err := scanner.Scan(&item.TaskID, &item.APIKeyID, &item.UpstreamID, &item.UpstreamProtocol, &item.Scenario, &item.Resolution, &status, &item.RetryCount, &item.ResultPublicURL, &item.ResultArtifactID, &item.Duration, &created, &started, &finished); err != nil {
		return domain.AdminTaskSummary{}, err
	}
	item.InternalStatus, item.Status = status, status.V2()
	item.CreatedAt = unix(created)
	if started > 0 {
		item.StartedAt = unix(started)
	}
	if finished > 0 {
		item.FinishedAt = unix(finished)
	}
	return item, nil
}

func internalStatuses(status domain.V2Status) []domain.InternalStatus {
	switch status {
	case domain.V2Queued:
		return []domain.InternalStatus{domain.StatusQueuedOpen, domain.StatusQueuedLocked}
	case domain.V2Running:
		return []domain.InternalStatus{domain.StatusDispatching, domain.StatusRunning, domain.StatusReconciling, domain.StatusCancelling}
	case domain.V2Succeeded:
		return []domain.InternalStatus{domain.StatusSucceeded}
	case domain.V2Failed:
		return []domain.InternalStatus{domain.StatusFailed}
	case domain.V2Cancelled:
		return []domain.InternalStatus{domain.StatusCancelled}
	default:
		return nil
	}
}

func (s *Store) nowUnix() int64   { return s.options.Now().UTC().Unix() }
func (s *Store) nowMillis() int64 { return s.options.Now().UTC().UnixMilli() }
func unix(value int64) time.Time  { return time.Unix(value, 0).UTC() }
