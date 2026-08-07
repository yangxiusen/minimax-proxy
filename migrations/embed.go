package migrations

import _ "embed"

// Initial 保存首版完整数据库结构，由服务启动时执行。
//
//go:embed 001_init.sql
var Initial string
