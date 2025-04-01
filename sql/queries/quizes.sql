-- name: CreateQuiz :one
INSERT INTO quizes (
    creator_id, title, description, visibility
) VALUES (
    $1, $2, $3, $4
)
RETURNING *;

-- name: GetQuizByID :one
SELECT
    q.id,
    q.creator_id,
    q.title,
    q.description,
    q.visibility,
    q.created_at,
    q.updated_at,
    u.name AS creator_name,
    u.picture AS creator_picture
FROM
    quizes q
JOIN
    users u ON q.creator_id = u.id
WHERE
    q.id = $1
LIMIT 1;

-- name: ListQuizes :many
SELECT * FROM quizes
ORDER BY created_at DESC;

-- name: ListQuizesByCreatorID :many
SELECT * FROM quizes
WHERE creator_id = $1
ORDER BY created_at DESC;

-- name: ListQuizesByVisibility :many
SELECT * FROM quizes
WHERE visibility = $1
ORDER BY created_at DESC;

-- name: ListPublicQuizes :many
SELECT * FROM quizes
WHERE visibility = 'public'
ORDER BY created_at DESC;

-- name: UpdateQuiz :one
UPDATE quizes
SET
    creator_id = $2, -- Use with caution if changing ownership
    title = $3,
    description = $4,
    visibility = $5
WHERE id = $1
RETURNING *;

-- name: DeleteQuiz :exec
DELETE FROM quizes
WHERE id = $1;

-- name: ListQuizzesByCreator :many
SELECT id, title, created_at, updated_at FROM quizes
WHERE creator_id = $1
ORDER BY created_at DESC;