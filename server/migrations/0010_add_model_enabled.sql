-- 模型是否发布到 Cursor 的模型目录:分组开关批量切换该标记,关闭的模型仍可用于已有会话。
ALTER TABLE model_configs ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1));
