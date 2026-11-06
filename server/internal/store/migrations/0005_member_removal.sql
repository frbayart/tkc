-- Purging a removed family member (after the evidence retention period, see
-- 0007) deletes the account and everything attached to it (devices,
-- sessions, relationships, their messages and calls). These two references
-- would otherwise block the deletion. See spec/identity-family.md.

-- A device approved by a removed parent's device stays active; only the
-- audit link is cleared.
ALTER TABLE devices DROP CONSTRAINT devices_approved_by_device_id_fkey;
ALTER TABLE devices ADD CONSTRAINT devices_approved_by_device_id_fkey
    FOREIGN KEY (approved_by_device_id) REFERENCES devices (id) ON DELETE SET NULL;

-- Messages sent from a removed member's devices are deleted with them.
ALTER TABLE messages DROP CONSTRAINT messages_sender_device_id_fkey;
ALTER TABLE messages ADD CONSTRAINT messages_sender_device_id_fkey
    FOREIGN KEY (sender_device_id) REFERENCES devices (id) ON DELETE CASCADE;
