# Protect edited task context files during removal

devtask stores digests of the Task Context Files it generates and removes those files without force only while their contents still match. New or edited Task Context Files are user-owned local data with no Git protection, so they block Task removal unless `--force` is explicit; devtask does not introduce a hidden archive whose lifecycle would create another source of truth.
