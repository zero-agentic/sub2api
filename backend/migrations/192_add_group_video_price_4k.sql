-- Seedance 2.0 Pro supports a distinct 4K output tier. Keep its group price
-- independent so 4K is never billed at the 1080p rate.

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS video_price_4k DECIMAL(20,8);

COMMENT ON COLUMN groups.video_price_4k IS '4K 视频生成每秒单价 (USD/s)，支持 4K 的视频模型使用';
COMMENT ON COLUMN usage_logs.video_resolution IS '计费用视频分辨率 480p/720p/1080p/4k';
