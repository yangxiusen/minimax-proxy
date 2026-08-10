package migrations

import _ "embed"

// Initial 保存首版完整数据库结构，由服务启动时执行。
//
//go:embed 001_init.sql
var Initial string

// ResolutionTiers 将历史 768P 档修正为 480P，并扩展允许的分辨率。
//
//go:embed 002_resolution_tiers.sql
var ResolutionTiers string
