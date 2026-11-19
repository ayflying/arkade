-- Personal MCP keys keep the existing storage and digests. Only transitions
-- into a disabled state or out of the admin role revoke all of an owner's keys.
-- Neither re-enable nor promotion restores previously revoked credentials.
DROP TRIGGER revoke_ineligible_admin_api_keys ON users;
CREATE TRIGGER revoke_ineligible_admin_api_keys
 AFTER UPDATE OF disabled, role ON users FOR EACH ROW
 WHEN ((OLD.disabled = 0 AND NEW.disabled <> 0)
       OR (OLD.role = 'admin' AND NEW.role <> 'admin'))
 EXECUTE FUNCTION revoke_ineligible_admin_api_keys();
