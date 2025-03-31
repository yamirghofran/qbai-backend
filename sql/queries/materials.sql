-- name: CreateMaterial :one
INSERT INTO materials (
    user_id, title, url
) VALUES (
    $1, $2, $3
)
RETURNING *;

-- name: GetMaterialByID :one
SELECT * FROM materials
WHERE id = $1 LIMIT 1;

-- name: ListMaterials :many
SELECT * FROM materials
ORDER BY created_at DESC;

-- name: ListMaterialsByUserID :many
SELECT * FROM materials
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: UpdateMaterial :one
UPDATE materials
SET
    user_id = $2, -- Use with caution if changing ownership
    title = $3,
    url = $4
WHERE id = $1
RETURNING *;

-- name: DeleteMaterial :exec
DELETE FROM materials
WHERE id = $1;