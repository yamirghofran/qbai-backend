-- name: CreateQuizAttempt :one
INSERT INTO quiz_attempts (quiz_id, user_id, start_time)
VALUES ($1, $2, NOW())
RETURNING *;

-- name: GetQuizAttempt :one
SELECT *
FROM quiz_attempts
WHERE id = $1;

-- name: UpdateQuizAttemptScoreAndEndTime :one
UPDATE quiz_attempts
SET score = $2, end_time = $3, updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: ListQuizAttemptsByUser :many
SELECT *
FROM quiz_attempts
WHERE user_id = $1
ORDER BY start_time DESC;

-- name: GetQuizAttemptWithDetails :one
SELECT
    qa.id AS attempt_id,
    qa.quiz_id,
    qa.user_id,
    qa.score,
    qa.start_time,
    qa.end_time,
    q.title AS quiz_title,
    (SELECT COUNT(*) FROM questions WHERE quiz_id = qa.quiz_id) AS total_questions,
    (SELECT COUNT(*) FROM attempt_answers WHERE quiz_attempt_id = qa.id) AS answered_questions
FROM
    quiz_attempts qa
JOIN
    quizes q ON qa.quiz_id = q.id
WHERE
    qa.id = $1;

-- name: ListQuizAttemptsWithDetailsByUser :many
SELECT
    qa.id AS attempt_id,
    qa.quiz_id,
    qa.user_id,
    qa.score,
    qa.start_time,
    qa.end_time,
    q.title AS quiz_title,
    (SELECT COUNT(*) FROM questions WHERE quiz_id = qa.quiz_id) AS total_questions,
    (SELECT COUNT(*) FROM attempt_answers WHERE quiz_attempt_id = qa.id) AS answered_questions
FROM
    quiz_attempts qa
JOIN
    quizes q ON qa.quiz_id = q.id
WHERE
    qa.user_id = $1
ORDER BY
    qa.start_time DESC;
-- name: ListUserAttemptsWithQuizName :many
SELECT
    qa.id AS attempt_id,
    qa.quiz_id, -- Added quiz_id
    qa.start_time,
    qa.score,
    q.title AS quiz_name,
    (SELECT COUNT(*) FROM questions WHERE quiz_id = q.id) AS total_questions -- Added total questions
FROM
    quiz_attempts qa
JOIN
    quizes q ON qa.quiz_id = q.id
WHERE
    qa.user_id = $1
ORDER BY
    qa.start_time DESC;