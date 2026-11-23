ALTER TABLE services ADD COLUMN container TEXT NOT NULL DEFAULT ''; -- linked Docker container name; '' = none
