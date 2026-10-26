package app.twocan.ui

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import app.twocan.Twocan
import app.twocan.userMessage
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import kotlinx.coroutines.launch

/** Add a contact by scanning their QR code or pasting their card text. */
@Composable
fun AddContactScreen(twocan: Twocan, onBack: () -> Unit) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var text by rememberSaveable { mutableStateOf("") }
    var error by rememberSaveable { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }

    fun add(card: String) {
        busy = true
        scope.launch {
            runCatching { twocan.call { it.addContact(card) } }
                .onSuccess { onBack() }
                .onFailure { error = it.userMessage }
            busy = false
        }
    }

    // The scanner asks for the camera permission itself.
    val scan = rememberLauncherForActivityResult(ScanContract()) { result ->
        result.contents?.let(::add)
    }

    Scaffold(topBar = { BackBar("Add contact", onBack) }) { padding ->
        Column(
            Modifier.fillMaxSize().padding(padding).imePadding().verticalScroll(rememberScrollState()).padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            Text("Scan the QR code on their My card screen. They will see your request and can accept it.")
            Button(
                onClick = {
                    error = null
                    scan.launch(
                        ScanOptions()
                            .setDesiredBarcodeFormats(ScanOptions.QR_CODE)
                            .setPrompt("Point the camera at their Twocan QR code")
                            .setBeepEnabled(false)
                            .setOrientationLocked(false)
                            .setCaptureActivity(ScanActivity::class.java),
                    )
                },
                enabled = !busy,
                modifier = Modifier.fillMaxWidth(),
            ) { Text("Scan QR code") }

            HorizontalDivider()

            Text("Or paste the card text they sent you:")
            OutlinedTextField(
                value = text,
                onValueChange = { text = it; error = null },
                label = { Text("twocan:…") },
                minLines = 3,
                modifier = Modifier.fillMaxWidth(),
            )
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedButton(onClick = { text = context.pasteText(); error = null }) { Text("Paste") }
                Button(onClick = { error = null; add(text) }, enabled = !busy && text.isNotBlank()) { Text("Add") }
            }
            error?.let { Text(it, color = MaterialTheme.colorScheme.error) }
        }
    }
}
