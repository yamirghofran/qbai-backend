-- name: CreateTopic :one
INSERT INTO topics (
    creator_id, title, description
) VALUES (
    $1, $2, $3
)
RETURNING *;

-- name: GetTopicByID :one
SELECT * FROM topics
WHERE id = $1 LIMIT 1;

-- name: ListTopics :many
SELECT * FROM topics
ORDER BY created_at DESC;

-- name: ListTopicsByCreatorID :many
SELECT * FROM topics
WHERE creator_id = $1
ORDER BY created_at DESC;

-- name: UpdateTopic :one
UPDATE topics
SET
    creator_id = $2, -- Use with caution if changing ownership
    title = $3,
    description = $4
WHERE id = $1
RETURNING *;

-- name: DeleteTopic :exec
DELETE FROM topics
WHERE id = $1;
 
-- name: GetTopicByTitleAndUser :one
SELECT id, creator_id, title, description, created_at, updated_at
FROM topics
WHERE title = $1 AND creator_id = $2;