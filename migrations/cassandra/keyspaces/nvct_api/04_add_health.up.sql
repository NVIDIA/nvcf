-- Adding health field to tasks table
ALTER TABLE nvct_api.tasks_v2 ADD IF NOT EXISTS health TEXT;
