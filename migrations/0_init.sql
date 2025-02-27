CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Create an enum for Lobby 'type'
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'lobby_type') THEN
        CREATE TYPE lobby_type AS ENUM ('private', 'public', 'matchmaking');
    END IF;
END$$;

-- ==============
--  USERS TABLE
-- ==============
CREATE TABLE IF NOT EXISTS users (
    id                UUID DEFAULT uuid_generate_v4() PRIMARY KEY,
    email             TEXT UNIQUE,
    password          TEXT,
    username          TEXT,
    is_ephemeral      BOOLEAN NOT NULL DEFAULT FALSE,
    is_admin          BOOLEAN NOT NULL DEFAULT FALSE,
    elo_1v1           INTEGER NOT NULL DEFAULT 1500,
    elo_4p            INTEGER NOT NULL DEFAULT 1500,
    elo_7p8p          INTEGER NOT NULL DEFAULT 1500,
    created_at        TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMP NOT NULL DEFAULT NOW(),

    -- Glicko2 storing for 1v1 mode:
    phi_1v1           FLOAT NOT NULL DEFAULT 350.0,
    sigma_1v1         FLOAT NOT NULL DEFAULT 0.06
);

-- ===================
--  FRIENDS RELATION
-- ===================
CREATE TABLE IF NOT EXISTS friends (
    user1_id   UUID REFERENCES users(id) ON DELETE CASCADE,
    user2_id   UUID REFERENCES users(id) ON DELETE CASCADE,
    status     TEXT NOT NULL DEFAULT 'pending',
    PRIMARY KEY (user1_id, user2_id),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- ==============
--  LOBBIES
-- ==============
CREATE TABLE IF NOT EXISTS lobbies (
    id                                UUID DEFAULT uuid_generate_v4() PRIMARY KEY,
    host_user_id                      UUID REFERENCES users(id) NOT NULL,
    type                              lobby_type NOT NULL,  -- 'private', 'public', 'matchmaking'
    mode                              TEXT,                 -- 'head_to_head', 'group_of_4', 'circuit_4p', 'circuit_7p8p', etc.
    house_rule_freeze_disconnect      BOOLEAN NOT NULL DEFAULT FALSE,
    house_rule_forfeit_disconnect     BOOLEAN NOT NULL DEFAULT FALSE,
    house_rule_missed_round_threshold SMALLINT NOT NULL DEFAULT 2,
    penalty_card_count                SMALLINT NOT NULL DEFAULT 2,
    allow_replaced_discard_abilities BOOLEAN NOT NULL DEFAULT FALSE,
    disconnection_threshold           SMALLINT NOT NULL DEFAULT 2,
    circuit_mode                      BOOLEAN NOT NULL DEFAULT FALSE,
    circuit_elimination_score         INTEGER,
    circuit_num_rounds                SMALLINT,
    false_cambia_penalty             SMALLINT DEFAULT 0,
    win_bonus                        SMALLINT DEFAULT 0,
    ranked                            BOOLEAN NOT NULL DEFAULT FALSE,
    ranking_mode                      TEXT,  -- '1v1', '4p', '7p8p', etc.
    start_time                        TIMESTAMP,
    end_time                          TIMESTAMP,
    created_at                        TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at                        TIMESTAMP NOT NULL DEFAULT NOW()
);

-- =======================
--  LOBBY PARTICIPANTS
-- =======================
CREATE TABLE IF NOT EXISTS lobby_participants (
    lobby_id       UUID REFERENCES lobbies(id) ON DELETE CASCADE,
    user_id        UUID REFERENCES users(id) ON DELETE CASCADE,
    is_ready       BOOLEAN NOT NULL DEFAULT FALSE,
    seat_position  SMALLINT,
    PRIMARY KEY (lobby_id, user_id),
    updated_at     TIMESTAMP NOT NULL DEFAULT NOW()
);

-- =========
--  GAMES
-- =========
CREATE TABLE IF NOT EXISTS games (
    id                  UUID DEFAULT uuid_generate_v4() PRIMARY KEY,
    lobby_id            UUID NOT NULL REFERENCES lobbies(id) ON DELETE CASCADE,
    round_index         SMALLINT NOT NULL DEFAULT 0,  -- which round (if circuit)
    status              TEXT NOT NULL,                -- 'in_progress', 'completed', 'abandoned'
    start_time          TIMESTAMP,
    end_time            TIMESTAMP,
    initial_game_state  JSONB,                        -- store partial info about initial deals, etc.
    created_at          TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMP NOT NULL DEFAULT NOW()
);

-- ===============
--  GAME ACTIONS
-- ===============
CREATE TABLE IF NOT EXISTS game_actions (
    id            UUID DEFAULT uuid_generate_v4() PRIMARY KEY,
    game_id       UUID NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    action_index  INTEGER NOT NULL,
    actor_user_id UUID REFERENCES users(id),
    action_type   TEXT NOT NULL,   -- e.g. 'draw', 'discard', 'snap', 'call_cambia'
    action_payload JSONB,          -- details about the move
    created_at    TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMP NOT NULL DEFAULT NOW()
);

-- ================
--  GAME RESULTS
-- ================
CREATE TABLE IF NOT EXISTS game_results (
    id         UUID DEFAULT uuid_generate_v4() PRIMARY KEY,
    game_id    UUID NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    player_id  UUID NOT NULL REFERENCES users(id),
    score      INTEGER,
    did_win    BOOLEAN,
    ranking    SMALLINT,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- ===========
--  RATINGS
-- ===========
CREATE TABLE IF NOT EXISTS ratings (
    id            UUID DEFAULT uuid_generate_v4() PRIMARY KEY,
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    game_id       UUID REFERENCES games(id) ON DELETE CASCADE,
    old_rating    INTEGER NOT NULL,
    new_rating    INTEGER NOT NULL,
    rating_mode   TEXT NOT NULL,  -- '1v1', '4p', '7p8p', etc.
    created_at    TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Sets updated_at to the current timestamp
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- updated at triggers on each table
CREATE TRIGGER set_updated_at_users
BEFORE UPDATE ON users
FOR EACH ROW
EXECUTE PROCEDURE set_updated_at();

CREATE TRIGGER set_updated_at_friends
BEFORE UPDATE ON friends
FOR EACH ROW
EXECUTE PROCEDURE set_updated_at();

CREATE TRIGGER set_updated_at_lobbies
BEFORE UPDATE ON lobbies
FOR EACH ROW
EXECUTE PROCEDURE set_updated_at();

CREATE TRIGGER set_updated_at_lobby_participants
BEFORE UPDATE ON lobby_participants
FOR EACH ROW
EXECUTE PROCEDURE set_updated_at();

CREATE TRIGGER set_updated_at_games
BEFORE UPDATE ON games
FOR EACH ROW
EXECUTE PROCEDURE set_updated_at();

CREATE TRIGGER set_updated_at_game_actions
BEFORE UPDATE ON game_actions
FOR EACH ROW
EXECUTE PROCEDURE set_updated_at();

CREATE TRIGGER set_updated_at_game_results
BEFORE UPDATE ON game_results
FOR EACH ROW
EXECUTE PROCEDURE set_updated_at();

CREATE TRIGGER set_updated_at_ratings
BEFORE UPDATE ON ratings
FOR EACH ROW
EXECUTE PROCEDURE set_updated_at();
