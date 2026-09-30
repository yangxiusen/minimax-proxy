package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"minimax-h3-tc/migrations"
)

const latestSchemaVersion = 22

// 关闭外键必须发生在事务外；恢复失败时丢弃连接，避免污染连接池。
func (s *Store) migrationConnection(ctx context.Context) (*sql.Conn, func(*error), error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	var originalFK int
	if err = conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&originalFK); err != nil {
		conn.Close()
		return nil, nil, err
	}
	inTransaction := false
	finish := func(result *error) {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if inTransaction {
			if *result == nil {
				*result = ctx.Err()
			}
			if *result == nil {
				*result = migrationForeignKeyCheck(cleanupCtx, conn)
			}
			if *result == nil {
				_, *result = conn.ExecContext(cleanupCtx, `COMMIT`)
			}
			if *result != nil {
				if _, rollbackErr := conn.ExecContext(cleanupCtx, `ROLLBACK`); rollbackErr != nil {
					*result = errors.Join(*result, rollbackErr)
					_ = conn.Raw(func(any) error { return driver.ErrBadConn })
				}
			}
		}
		_, restoreErr := conn.ExecContext(cleanupCtx, fmt.Sprintf("PRAGMA foreign_keys=%d", originalFK))
		var restored int
		if restoreErr == nil {
			restoreErr = conn.QueryRowContext(cleanupCtx, `PRAGMA foreign_keys`).Scan(&restored)
		}
		if restoreErr == nil && restored != originalFK {
			restoreErr = errors.New("数据库迁移后外键设置未恢复")
		}
		if *result == nil && restoreErr == nil {
			restoreErr = migrationForeignKeyCheck(cleanupCtx, conn)
		}
		if restoreErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		*result = errors.Join(*result, restoreErr, conn.Close())
	}
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err == nil {
		_, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`)
		inTransaction = err == nil
	}
	if err == nil {
		err = checkMigrationVersion(ctx, conn)
	}
	if err != nil {
		finish(&err)
		return nil, nil, err
	}
	return conn, finish, nil
}

func checkMigrationVersion(ctx context.Context, conn *sql.Conn) error {
	var userVersion, ledgerVersion, exists int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		return err
	}
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&ledgerVersion); err != nil {
			return err
		}
	}
	if max(userVersion, ledgerVersion) > latestSchemaVersion {
		return fmt.Errorf("数据库版本 %d 超过支持版本 %d", max(userVersion, ledgerVersion), latestSchemaVersion)
	}
	return nil
}

func migrationForeignKeyCheck(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("数据库迁移外键完整性校验失败")
	}
	return rows.Err()
}

func (s *Store) migrateV22(ctx context.Context) (err error) {
	conn, finish, err := s.migrationConnection(ctx)
	if err != nil {
		return err
	}
	defer finish(&err)
	var applied int
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version=22`).Scan(&applied); err != nil {
		return err
	}
	if applied != 0 {
		return nil
	}
	counts, err := migrationTableCounts(ctx, conn)
	if err != nil {
		return err
	}
	columns, err := rebuildV22Tasks(ctx, conn)
	if err != nil {
		return fmt.Errorf("重建022任务表: %w", err)
	}
	const originalNodeColumns = `id,service_url,protocol_version,upstream_model,api_key_ciphertext,api_key_nonce,api_key_fingerprint,api_key_id`
	if _, err = conn.ExecContext(ctx, `CREATE TEMP TABLE migration_v22_original_nodes AS SELECT `+originalNodeColumns+` FROM model_service_nodes`); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, migrations.MultiModelProtocolRouting); err != nil {
		return err
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	instance := fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	if _, err = conn.ExecContext(ctx, `INSERT INTO routing_state(id,instance_id,updated_at) VALUES(1,?,?)`, instance, s.nowUnix()); err != nil {
		return err
	}
	nodes, err := s.backfillV22Catalogs(ctx, conn)
	if err != nil {
		return err
	}
	if err = s.backfillV22Tasks(ctx, conn, nodes); err != nil {
		return err
	}
	for table, before := range counts {
		var after int64
		if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM `+migrationIdentifier(table)).Scan(&after); err != nil {
			return err
		}
		if before != after {
			return fmt.Errorf("迁移改变原表行数: %s", table)
		}
	}
	var changed int
	if err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT `+columns+` FROM video_tasks EXCEPT SELECT `+columns+` FROM temp.migration_v22_original_tasks)`).Scan(&changed); err != nil {
		return err
	}
	if changed != 0 {
		return errors.New("迁移改变原任务数据")
	}
	if err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT `+originalNodeColumns+` FROM model_service_nodes EXCEPT SELECT `+originalNodeColumns+` FROM temp.migration_v22_original_nodes)`).Scan(&changed); err != nil {
		return err
	}
	if changed != 0 {
		return errors.New("迁移改变原节点连接或凭据")
	}
	if _, err = conn.ExecContext(ctx, `DROP TABLE temp.migration_v22_original_tasks; DROP TABLE temp.migration_v22_original_nodes; INSERT INTO schema_migrations(version,applied_at) VALUES(22,?); PRAGMA user_version=22`, s.nowMillis()); err != nil {
		return err
	}
	return nil
}

func migrationIdentifier(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func migrationTableCounts(ctx context.Context, conn *sql.Conn) (map[string]int64, error) {
	rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, name)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(names))
	for _, name := range names {
		var count int64
		if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM `+migrationIdentifier(name)).Scan(&count); err != nil {
			return nil, err
		}
		counts[name] = count
	}
	return counts, nil
}

func rebuildV22Tasks(ctx context.Context, conn *sql.Conn) (string, error) {
	var ddl string
	if err := conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='video_tasks'`).Scan(&ddl); err != nil {
		return "", err
	}
	start := strings.Index(ddl, "(")
	if start < 0 {
		return "", errors.New("未知任务表结构")
	}
	ddl = "CREATE TABLE video_tasks_v22 " + ddl[start:]
	for old, replacement := range map[string]string{
		"resolution TEXT NOT NULL CHECK (resolution IN ('480P','768P','2K'))": "resolution TEXT NOT NULL DEFAULT '' CHECK (length(resolution)<=32)",
		"duration INTEGER NOT NULL CHECK (duration BETWEEN 4 AND 15)":         "duration INTEGER NOT NULL CHECK (duration>0)",
	} {
		if strings.Count(ddl, old) != 1 {
			return "", errors.New("任务表约束与021基线不匹配")
		}
		ddl = strings.Replace(ddl, old, replacement, 1)
	}
	rows, err := conn.QueryContext(ctx, `SELECT name FROM pragma_table_info('video_tasks') ORDER BY cid`)
	if err != nil {
		return "", err
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return "", err
		}
		names = append(names, migrationIdentifier(name))
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return "", err
	}
	columns := strings.Join(names, ",")
	rows, err = conn.QueryContext(ctx, `SELECT sql FROM sqlite_master WHERE tbl_name='video_tasks' AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY type,name`)
	if err != nil {
		return "", err
	}
	var objects []string
	for rows.Next() {
		var object string
		if err = rows.Scan(&object); err != nil {
			rows.Close()
			return "", err
		}
		objects = append(objects, object)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return "", err
	}
	var sequence int64
	if err = conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM sqlite_sequence WHERE name='video_tasks'`).Scan(&sequence); err != nil {
		return "", err
	}
	// 保留原始列声明及约束，只替换明确允许放宽的两处；显式列清单避免遗漏历史列。
	for _, statement := range []string{ddl, `CREATE TEMP TABLE migration_v22_original_tasks AS SELECT ` + columns + ` FROM video_tasks`,
		`INSERT INTO video_tasks_v22 (` + columns + `) SELECT ` + columns + ` FROM video_tasks`,
		`DROP TABLE video_tasks`, `ALTER TABLE video_tasks_v22 RENAME TO video_tasks`} {
		if _, err = conn.ExecContext(ctx, statement); err != nil {
			return "", err
		}
	}
	for _, object := range objects {
		if _, err = conn.ExecContext(ctx, object); err != nil {
			return "", err
		}
	}
	if _, err = conn.ExecContext(ctx, `UPDATE sqlite_sequence SET seq=MAX(seq,?) WHERE name='video_tasks'`, sequence); err != nil {
		return "", err
	}
	return columns, nil
}

type migrationV22Node struct {
	id, protocol, serviceURL, fingerprint, alias, capabilities string
	version                                                    int64
	enabled                                                    bool
	deleted                                                    sql.NullInt64
}

func (s *Store) backfillV22Catalogs(ctx context.Context, conn *sql.Conn) (map[string]migrationV22Node, error) {
	rows, err := conn.QueryContext(ctx, `SELECT id,protocol_version,service_url,COALESCE(api_key_fingerprint,''),upstream_model,capabilities_json,version,enabled,deleted_at FROM model_service_nodes`)
	if err != nil {
		return nil, err
	}
	nodes := map[string]migrationV22Node{}
	for rows.Next() {
		var n migrationV22Node
		if err = rows.Scan(&n.id, &n.protocol, &n.serviceURL, &n.fingerprint, &n.alias, &n.capabilities, &n.version, &n.enabled, &n.deleted); err != nil {
			rows.Close()
			return nil, err
		}
		nodes[n.id] = n
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	protocols := map[string]bool{}
	now := s.nowUnix()
	for _, n := range nodes {
		source, verified := "builtin", 1
		switch n.protocol {
		case "h3-node-v1", "legacy-gradio-v1":
		case "minimax-v2":
			source, verified = "manual", 0
		default:
			continue
		}
		if n.protocol == "minimax-v2" && strings.TrimSpace(n.alias) != "" && n.alias != "MiniMax-H3" {
			if _, err = conn.ExecContext(ctx, `UPDATE model_service_nodes SET legacy_model_compat=1 WHERE id=?`, n.id); err != nil {
				return nil, err
			}
		}
		fingerprint := sha256.Sum256([]byte(n.protocol + "\x00" + n.serviceURL + "\x00" + n.fingerprint))
		if _, err = conn.ExecContext(ctx, `INSERT INTO node_model_catalogs(node_id,source,connection_fingerprint,last_status,last_attempt_at,last_success_at,created_at,updated_at) VALUES(?,?,?,'ready',?,?,?,?)`, n.id, source, hex.EncodeToString(fingerprint[:]), now, now, now, now); err != nil {
			return nil, err
		}
		capabilities, err := json.Marshal(migrationV22Capabilities(n))
		if err != nil {
			return nil, err
		}
		if _, err = conn.ExecContext(ctx, `INSERT INTO node_models(node_id,model_id,enabled,present,capabilities_json,verified,created_at,updated_at) VALUES(?,'MiniMax-H3',1,1,?,?,?,?)`, n.id, string(capabilities), verified, now, now); err != nil {
			return nil, err
		}
		if n.enabled && !n.deleted.Valid {
			protocols[n.protocol] = true
		}
	}
	var selected any
	mode := "unresolved"
	if len(protocols) == 1 {
		mode = "auto"
		for protocol := range protocols {
			selected = protocol
		}
	}
	if len(nodes) > 0 {
		_, err = conn.ExecContext(ctx, `INSERT INTO model_routes(model_id,protocol_version,selection_mode,created_at,updated_at) VALUES('MiniMax-H3',?,?,?,?)`, selected, mode, now, now)
	}
	return nodes, err
}

func migrationV22Capabilities(n migrationV22Node) map[string]any {
	scenarios := []string{"t2va", "i2va", "r2va"}
	if n.protocol == "h3-node-v1" {
		var old struct {
			Scenarios []string `json:"scenarios"`
		}
		if json.Unmarshal([]byte(n.capabilities), &old) == nil {
			scenarios = nil
			for _, scenario := range old.Scenarios {
				if slices.Contains([]string{"t2v", "i2v", "r2v"}, scenario) {
					scenarios = append(scenarios, scenario+"a")
				}
			}
		}
	}
	durations := []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	modes := []map[string]any{}
	for _, scenario := range scenarios {
		roles := []string{}
		if scenario == "i2va" {
			roles = []string{"first_frame", "last_frame"}
		} else if scenario == "r2va" {
			roles = []string{"reference_image", "reference_video", "reference_audio"}
		}
		modes = append(modes, map[string]any{"scenario": scenario, "durations": durations, "roles": roles})
	}
	capability := map[string]any{"schema_version": 1, "modes": modes, "input_sources": []string{"http", "https", "data"}, "resolution_policy": "required", "max_media": 15}
	if n.protocol == "minimax-v2" {
		capability["resolutions"] = []string{"768P", "2K"}
		capability["input_sources"] = []string{"http", "https", "data", "mm_file"}
	}
	return capability
}

type migrationV22Task struct {
	id, model, scenario, status, request, resolution, ratio string
	duration                                                int
	node, job, event, config                                sql.NullString
	baseline, slot                                          int
	attempt                                                 sql.NullInt64
}

func (s *Store) backfillV22Tasks(ctx context.Context, conn *sql.Conn, nodes map[string]migrationV22Node) error {
	// 请求可能含大段内联媒体，逐行读取以免同时持有多份请求；更新前先关闭游标。
	var after int64
	for {
		rows, err := conn.QueryContext(ctx, `SELECT queue_seq,task_id,model,scenario,status,request_json,resolution,duration,ratio_requested,upstream_id,upstream_job_id,gradio_event_id,official_submission_baseline_saved,upstream_slot_active,attempt_started_at,config_snapshot_json FROM video_tasks WHERE queue_seq>? ORDER BY queue_seq LIMIT 1`, after)
		if err != nil {
			return err
		}
		var batch []migrationV22Task
		for rows.Next() {
			var t migrationV22Task
			if err = rows.Scan(&after, &t.id, &t.model, &t.scenario, &t.status, &t.request, &t.resolution, &t.duration, &t.ratio, &t.node, &t.job, &t.event, &t.baseline, &t.slot, &t.attempt, &t.config); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, t)
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, t := range batch {
			if err = s.backfillV22Task(ctx, conn, nodes, t); err != nil {
				return err
			}
		}
	}
}

func (s *Store) backfillV22Task(ctx context.Context, conn *sql.Conn, nodes map[string]migrationV22Node, t migrationV22Task) error {
	terminal := slices.Contains([]string{"succeeded", "failed", "cancelled"}, t.status)
	queued := t.status == "queued_open" || t.status == "queued_locked"
	evidence := t.node.Valid || t.job.Valid || t.event.Valid || t.baseline != 0 || t.slot != 0 || t.attempt.Valid
	protocols := map[string]bool{}
	unknown := false
	var owner *migrationV22Node
	addNode := func(id string) {
		n, ok := nodes[id]
		if !ok || !slices.Contains([]string{"h3-node-v1", "legacy-gradio-v1", "minimax-v2"}, n.protocol) {
			unknown = true
			return
		}
		protocols[n.protocol] = true
		if owner == nil {
			owner = &n
		}
	}
	if t.node.Valid {
		addNode(t.node.String)
	}
	rows, err := conn.QueryContext(ctx, `SELECT a.node_id, a.status, a.finished_at FROM stage_attempts a JOIN task_stages s ON s.id=a.stage_id WHERE s.task_id=? UNION ALL SELECT b.node_id,'unknown',NULL FROM node_dispatch_barriers b WHERE b.task_id=?`, t.id, t.id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var node, status string
		var finished sql.NullInt64
		if err = rows.Scan(&node, &status, &finished); err != nil {
			rows.Close()
			return err
		}
		evidence = true
		if !finished.Valid || slices.Contains([]string{"dispatching", "running", "validating", "unknown"}, status) {
			addNode(node)
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	var oldBaseline string
	if err = conn.QueryRowContext(ctx, `SELECT upstream_jobs_before_json FROM video_tasks WHERE task_id=?`, t.id).Scan(&oldBaseline); err != nil {
		return err
	}
	if strings.TrimSpace(oldBaseline) != "[]" {
		evidence = true
	}
	state, reason, protocol := "awaiting_route", "legacy_route_unresolved", ""
	if terminal {
		state, reason = "legacy_unknown", "legacy_protocol_unknown"
	}
	if len(protocols) == 1 && !unknown {
		for p := range protocols {
			protocol = p
		}
		state, reason = "ready", ""
	} else if !terminal && (evidence || !queued) {
		state, reason = "migration_blocked", "legacy_submission_unresolved"
	}
	if len(protocols) > 1 && !terminal {
		state, reason = "migration_blocked", "legacy_protocol_conflict"
		protocol = ""
	}
	if !evidence && queued {
		eligible := map[string]bool{}
		for _, n := range nodes {
			if n.enabled && !n.deleted.Valid && migrationV22Supports(n, t) {
				valid, err := migrationV22ValidateRequest(ctx, conn, t)
				if err != nil {
					return err
				}
				if valid {
					eligible[n.protocol] = true
				}
			}
		}
		if len(eligible) == 1 {
			for p := range eligible {
				protocol = p
			}
			state, reason = "ready", ""
		}
	}
	var routing, dispatch any
	if state == "ready" {
		plan := "h3_profile"
		if protocol == "minimax-v2" {
			plan = "direct"
		}
		data, err := json.Marshal(map[string]any{"schema_version": 1, "model": t.model, "protocol_version": protocol, "route_version": 1, "routing_revision": 1, "selection_mode": "auto", "parameter_plan": plan, "normalizer_version": "legacy-h3-v1", "requirements": migrationV22Requirements(t)})
		if err != nil {
			return err
		}
		routing = string(data)
		if owner != nil {
			model := t.model
			if protocol == "minimax-v2" && strings.TrimSpace(owner.alias) != "" {
				model = owner.alias
			}
			data, err = json.Marshal(map[string]any{"schema_version": 1, "node_id": owner.id, "node_version": owner.version, "protocol_version": protocol, "service_url": owner.serviceURL, "upstream_model": model})
			if err != nil {
				return err
			}
			dispatch = string(data)
		}
	}
	var savedProtocol any
	if protocol != "" {
		savedProtocol = protocol
	}
	_, err = conn.ExecContext(ctx, `UPDATE video_tasks SET protocol_version=?,route_state=?,routing_snapshot_json=?,dispatch_snapshot_json=?,routing_wait_reason=? WHERE task_id=?`, savedProtocol, state, routing, dispatch, reason, t.id)
	return err
}

func migrationV22Supports(n migrationV22Node, t migrationV22Task) bool {
	if t.model != "MiniMax-H3" || t.duration < 4 || t.duration > 15 || !slices.Contains([]string{"480P", "768P", "2K"}, t.resolution) {
		return false
	}
	switch n.protocol {
	case "minimax-v2":
		return slices.Contains([]string{"768P", "2K"}, t.resolution)
	case "legacy-gradio-v1":
		return t.scenario == "t2va" || t.scenario == "i2va" || t.scenario == "r2va"
	case "h3-node-v1":
		if !t.config.Valid {
			return false
		}
		var caps struct {
			Stages    []string `json:"stages"`
			Scenarios []string `json:"scenarios"`
			Ratios    []string `json:"ratios"`
		}
		if json.Unmarshal([]byte(n.capabilities), &caps) != nil {
			return false
		}
		return slices.Contains(caps.Stages, "generation") && slices.Contains(caps.Scenarios, strings.TrimSuffix(t.scenario, "a")) && slices.Contains(caps.Ratios, t.ratio)
	}
	return false
}

func migrationV22Requirements(t migrationV22Task) map[string]any {
	requirements := map[string]any{"model": t.model, "scenario": t.scenario, "duration": t.duration, "resolution": t.resolution, "ratio": t.ratio}
	var request struct {
		Content []map[string]json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal([]byte(t.request), &request)
	roles, sources := []string{}, []string{}
	counts := map[string]int{"image": 0, "video": 0, "audio": 0}
	count := 0
	for _, item := range request.Content {
		var kind, role string
		_ = json.Unmarshal(item["type"], &kind)
		if !slices.Contains([]string{"image_url", "video_url", "audio_url"}, kind) {
			continue
		}
		count++
		counts[strings.TrimSuffix(kind, "_url")]++
		_ = json.Unmarshal(item["role"], &role)
		if role == "" && kind == "image_url" {
			role = "first_frame"
		}
		roles = append(roles, role)
		var media struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(item[kind], &media)
		source, _, _ := strings.Cut(media.URL, ":")
		if source == "proxy-input" {
			source = "data"
		}
		sources = append(sources, source)
	}
	requirements["roles"], requirements["input_sources"] = roles, sources
	requirements["media_count"], requirements["media_counts"] = count, counts
	return requirements
}

func migrationV22ValidateRequest(ctx context.Context, conn *sql.Conn, t migrationV22Task) (bool, error) {
	var request struct {
		Model      string `json:"model"`
		Duration   int    `json:"duration"`
		Resolution string `json:"resolution"`
		Ratio      string `json:"ratio"`
		Content    []struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Role  string `json:"role"`
			Image *struct {
				URL string `json:"url"`
			} `json:"image_url"`
			Video *struct {
				URL string `json:"url"`
			} `json:"video_url"`
			Audio *struct {
				URL string `json:"url"`
			} `json:"audio_url"`
		} `json:"content"`
	}
	if json.Unmarshal([]byte(t.request), &request) != nil || request.Model != t.model || request.Duration != t.duration || request.Resolution != t.resolution || len(request.Content) < 1 || len(request.Content) > 16 {
		return false, nil
	}
	textCount, first, last, images, videos, audios := 0, 0, 0, 0, 0, 0
	for index, item := range request.Content {
		var raw string
		role := item.Role
		switch item.Type {
		case "text":
			if strings.TrimSpace(item.Text) == "" || utf8.RuneCountInString(item.Text) > 14000 || item.Role != "" || item.Image != nil || item.Video != nil || item.Audio != nil {
				return false, nil
			}
			textCount++
			continue
		case "image_url":
			if item.Image == nil || item.Video != nil || item.Audio != nil || item.Text != "" {
				return false, nil
			}
			raw = item.Image.URL
			if role == "" {
				role = "first_frame"
			}
			switch role {
			case "first_frame":
				first++
			case "last_frame":
				last++
			case "reference_image":
				images++
			default:
				return false, nil
			}
		case "video_url":
			if item.Video == nil || item.Image != nil || item.Audio != nil || item.Text != "" || role != "reference_video" {
				return false, nil
			}
			raw = item.Video.URL
			videos++
		case "audio_url":
			if item.Audio == nil || item.Image != nil || item.Video != nil || item.Text != "" || role != "reference_audio" {
				return false, nil
			}
			raw = item.Audio.URL
			audios++
		default:
			return false, nil
		}
		u, err := url.Parse(raw)
		if err != nil || u.User != nil {
			return false, nil
		}
		switch u.Scheme {
		case "http", "https":
			if u.Host == "" {
				return false, nil
			}
		case "mm_file":
			if u.Host == "" && u.Opaque == "" {
				return false, nil
			}
		case "proxy-input":
			if u.Host != t.id || u.RawQuery != "" || u.Fragment != "" || u.Path == "" || strings.Contains(u.Path[1:], "/") {
				return false, nil
			}
			var count int
			err = conn.QueryRowContext(ctx, `SELECT count(*) FROM task_input_spool_files WHERE id=? AND task_id=? AND content_index=? AND content_type=? AND role=? AND source_kind IN ('data_uri','object_storage') AND size_bytes>0 AND length(sha256)=64`, strings.TrimPrefix(u.Path, "/"), t.id, index, item.Type, role).Scan(&count)
			if err != nil {
				return false, err
			}
			if count != 1 {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	if textCount != 1 || first > 1 || last > 1 || images > 9 || videos > 3 || audios > 3 || first+last > 0 && images+videos+audios > 0 {
		return false, nil
	}
	scenario := "t2va"
	if first+last > 0 {
		scenario = "i2va"
	} else if images+videos+audios > 0 {
		scenario = "r2va"
	}
	if scenario != t.scenario {
		return false, nil
	}
	if !slices.Contains([]string{"adaptive", "16:9", "9:16", "1:1", "4:3", "3:4", "21:9"}, t.ratio) {
		return false, nil
	}
	if scenario == "t2va" && t.ratio == "adaptive" {
		return false, nil
	}
	return true, nil
}
