-- name: CreateQuestion :one
INSERT INTO questions (
    quiz_id, topic_id, question
) VALUES (
    $1, $2, $3
)
RETURNING *;

-- name: GetQuestionByID :one
SELECT * FROM questions
WHERE id = $1 LIMIT 1;

-- name: ListQuestions :many
SELECT * FROM questions
ORDER BY created_at ASC; -- Or by position/order if added

-- name: ListQuestionsByQuizID :many
SELECT
    q.*,
    t.title AS topic_title
FROM
    questions q
LEFT JOIN
    topics t ON q.topic_id = t.id
WHERE
    q.quiz_id = $1
ORDER BY
    q.created_at ASC;

-- name: ListQuestionsByTopicID :many
SELECT * FROM questions
WHERE topic_id = $1
ORDER BY created_at ASC;

-- name: ListQuestionsByQuizAndTopicID :many
SELECT * FROM questions
WHERE quiz_id = $1 AND topic_id = $2
ORDER BY created_at ASC;

-- name: UpdateQuestion :one
UPDATE questions
SET
    quiz_id = $2,
    topic_id = $3,
    question = $4
WHERE id = $1
RETURNING *;

-- name: DeleteQuestion :exec
DELETE FROM questions
WHERE id = $1;