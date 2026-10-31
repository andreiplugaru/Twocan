package app.twocan.ui

import android.content.Context
import android.content.Intent
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.core.content.FileProvider
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import app.twocan.BuildConfig
import app.twocan.Twocan
import app.twocan.userMessage
import java.io.File
import kotlinx.coroutines.launch

/** Must match the provider's authority in AndroidManifest.xml. */
private const val FILES_AUTHORITY = "app.twocan.files"

/** The log's file name in the data dir (logging.FileName in Go). */
private const val LOG_FILE = "twocan.log"

@Composable
fun SettingsScreen(twocan: Twocan, onBack: () -> Unit) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val savedName by twocan.displayName.collectAsStateWithLifecycle()
    val reach by twocan.reachability.collectAsStateWithLifecycle()
    var name by rememberSaveable(savedName) { mutableStateOf(savedName) }
    var nameError by rememberSaveable { mutableStateOf<String?>(null) }
    var bootstrap by rememberSaveable { mutableStateOf(twocan.bootstrap) }
    var peerId by remember { mutableStateOf("") }
    var sendingLog by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(Unit) { peerId = runCatching { twocan.call { it.peerID() } }.getOrDefault("") }

    Scaffold(topBar = { BackBar("Settings", onBack) }) { padding ->
        Column(
            Modifier.fillMaxSize().padding(padding).imePadding().verticalScroll(rememberScrollState()).padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text("Your name", style = MaterialTheme.typography.titleMedium)
            OutlinedTextField(
                value = name,
                onValueChange = { name = it; nameError = null },
                singleLine = true,
                isError = nameError != null,
                supportingText = { Text(nameError ?: "Contacts who add you from now on see this name.") },
                modifier = Modifier.fillMaxWidth(),
            )
            Button(
                onClick = {
                    scope.launch {
                        nameError = runCatching { twocan.setDisplayName(name) }.exceptionOrNull()?.userMessage
                    }
                },
                enabled = name.isNotBlank() && name != savedName,
            ) { Text("Save name") }

            HorizontalDivider()
            Text("Your peer ID", style = MaterialTheme.typography.titleMedium)
            SelectionContainer { Text(peerId, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall) }
            Text("You are reachable: $reach")

            HorizontalDivider()
            Text("Helper addresses", style = MaterialTheme.typography.titleMedium)
            Text(
                "The helper finds your contacts and relays when a direct connection fails. " +
                    "Leave this empty to use the helper built into this app.",
                style = MaterialTheme.typography.bodyMedium,
            )
            OutlinedTextField(
                value = bootstrap,
                onValueChange = { bootstrap = it },
                placeholder = { Text("/ip4/…/tcp/4001/p2p/12D3KooW…, …") },
                minLines = 2,
                modifier = Modifier.fillMaxWidth(),
            )
            Button(
                onClick = {
                    twocan.bootstrap = bootstrap
                    twocan.restart()
                },
                enabled = bootstrap.trim() != twocan.bootstrap,
            ) { Text("Save and restart") }

            HorizontalDivider()
            Text("Troubleshooting", style = MaterialTheme.typography.titleMedium)
            Text(
                "The log records connections, discovery and delivery, never message text or keys. " +
                    "It does hold peer IDs and IP addresses.",
                style = MaterialTheme.typography.bodyMedium,
            )
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(onClick = { sendingLog = true }) { Text("Send log") }
                OutlinedButton(onClick = { context.shareLog(twocan.dataDir) }) { Text("Share log") }
            }
            Text("Twocan ${BuildConfig.VERSION_NAME}", style = MaterialTheme.typography.bodySmall)
        }
    }
    if (sendingLog) SendLogDialog(twocan) { sendingLog = false }
}

/** Shares the log file through Android's share sheet. */
private fun Context.shareLog(dataDir: File) {
    val log = File(dataDir, LOG_FILE)
    if (!log.exists()) return
    val uri = FileProvider.getUriForFile(this, FILES_AUTHORITY, log)
    val send = Intent(Intent.ACTION_SEND)
        .setType("text/plain")
        .putExtra(Intent.EXTRA_STREAM, uri)
        .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
    startActivity(Intent.createChooser(send, "Share Twocan log"))
}

/** The longest note Send log takes (as the Go core). */
private const val MAX_NOTE = 500

/**
 * Sends the log to the helper with an optional note, then shows the report's
 * code to pass on, or why it failed with Share log as the way out.
 */
@Composable
private fun SendLogDialog(twocan: Twocan, onDismiss: () -> Unit) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var note by rememberSaveable { mutableStateOf("") }
    var sending by remember { mutableStateOf(false) }
    var code by rememberSaveable { mutableStateOf<String?>(null) }
    var error by rememberSaveable { mutableStateOf<String?>(null) }

    val done = code
    val failed = error
    when {
        done != null -> AlertDialog(
            onDismissRequest = onDismiss,
            title = { Text("Log sent") },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    SelectionContainer {
                        Text("Code: $done", fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.titleMedium)
                    }
                    Text("Pass the code on to whoever looks into the problem.")
                }
            },
            confirmButton = { TextButton(onClick = { context.copyText("Twocan log code", done) }) { Text("Copy") } },
            dismissButton = { TextButton(onClick = onDismiss) { Text("Close") } },
        )
        failed != null -> AlertDialog(
            onDismissRequest = onDismiss,
            title = { Text("Could not send the log") },
            text = { Text(failed) },
            confirmButton = {
                TextButton(onClick = { context.shareLog(twocan.dataDir); onDismiss() }) { Text("Share log instead") }
            },
            dismissButton = { TextButton(onClick = onDismiss) { Text("Close") } },
        )
        else -> AlertDialog(
            onDismissRequest = { if (!sending) onDismiss() },
            title = { Text("Send log") },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(
                        "Sends your log to the helper, so the problem can be looked into. " +
                            "Your log, including contact names and network addresses. Never your messages.",
                    )
                    OutlinedTextField(
                        value = note,
                        onValueChange = { note = it.take(MAX_NOTE) },
                        placeholder = { Text("What went wrong? (optional)") },
                        minLines = 2,
                        enabled = !sending,
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
            },
            confirmButton = {
                TextButton(
                    enabled = !sending,
                    onClick = {
                        sending = true
                        scope.launch {
                            runCatching { twocan.call { it.sendLog(note) } }
                                .onSuccess { code = it }
                                .onFailure { error = it.userMessage }
                            sending = false
                        }
                    },
                ) { Text(if (sending) "Sending…" else "Send") }
            },
            dismissButton = { TextButton(enabled = !sending, onClick = onDismiss) { Text("Cancel") } },
        )
    }
}
