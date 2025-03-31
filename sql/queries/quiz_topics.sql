-- name: LinkQuizTopic :one
INSERT INTO quiz_topics (
    quiz_id, topic_id
) VALUES (
    $1, $2
)
RETURNING *;

-- name: GetQuizTopicByID :one
-- Less common to fetch by its own ID, but included for completeness
SELECT * FROM quiz_topics
WHERE id = $1 LIMIT 1;

-- name: GetQuizTopicByQuizAndTopicID :one
SELECT * FROM quiz_topics
WHERE quiz_id = $1 AND topic_id = $2 LIMIT 1;

-- name: ListQuizTopicsByQuizID :many
SELECT * FROM quiz_topics
WHERE quiz_id = $1;

-- name: ListTopicIDsByQuizID :many
SELECT topic_id FROM quiz_topics
WHERE quiz_id = $1;

-- name: ListQuizIDsByTopicID :many
SELECT quiz_id FROM quiz_topics
WHERE topic_id = $1;

-- name: UnlinkQuizTopic :exec
DELETE FROM quiz_topics
WHERE quiz_id = $1 AND topic_id = $2;

-- name: UnlinkAllTopicsFromQuiz :exec
DELETE FROM quiz_topics
WHERE quiz_id = $1;

-- name: UnlinkTopicFromAllQuizes :exec
DELETE FROM quiz_topics
WHERE topic_id = $1;