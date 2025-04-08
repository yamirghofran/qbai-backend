-- name: CreateFeedback :one
INSERT INTO feedback (
    user_id,
    content,
    rating
) VALUES (
    $1, $2, $3
)
RETURNING *;

-- name: GetFeedback :one
SELECT * FROM feedback
WHERE id = $1 LIMIT 1;

-- name: ListFeedbacks :many
SELECT * FROM feedback
ORDER BY created_at DESC;

-- name: UpdateFeedback :one
UPDATE feedback
SET
    content = $2,
    rating = $3,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteFeedback :exec
DELETE FROM feedback
WHERE id = $1;