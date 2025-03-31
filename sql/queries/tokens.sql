-- name: CreateToken :one
INSERT INTO tokens (
    user_id, amount, type
) VALUES (
    $1, $2, $3
)
RETURNING *;

-- name: GetTokenByID :one
SELECT * FROM tokens
WHERE id = $1 LIMIT 1;

-- name: ListTokens :many
SELECT * FROM tokens
ORDER BY created_at DESC;

-- name: ListTokensByUserID :many
SELECT * FROM tokens
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: UpdateToken :one
UPDATE tokens
SET
    user_id = $2, -- You might not want to update user_id often
    amount = $3,
    type = $4
WHERE id = $1
RETURNING *;

-- name: DeleteToken :exec
DELETE FROM tokens
WHERE id = $1;
