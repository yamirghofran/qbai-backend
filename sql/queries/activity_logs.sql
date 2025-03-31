-- name: CreateActivityLog :one
INSERT INTO activity_logs (
    user_id, action, target_type, target_id, details
) VALUES (
    $1, $2, $3, $4, $5
)
RETURNING *;

-- name: GetActivityLogByID :one
SELECT * FROM activity_logs
WHERE id = $1 LIMIT 1;

-- name: ListActivityLogs :many
SELECT * FROM activity_logs
ORDER BY created_at DESC;

-- name: ListActivityLogsByUserID :many
SELECT * FROM activity_logs
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: ListActivityLogsByAction :many
SELECT * FROM activity_logs
WHERE action = $1
ORDER BY created_at DESC;

-- name: ListActivityLogsByTarget :many
SELECT * FROM activity_logs
WHERE target_type = $1 AND target_id = $2
ORDER BY created_at DESC;

-- name: DeleteActivityLog :exec
-- Use with caution, logs are often meant to be kept
DELETE FROM activity_logs
WHERE id = $1;
