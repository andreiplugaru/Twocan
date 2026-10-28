package app.twocan.ui

import android.graphics.BitmapFactory
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.FilterQuality
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import app.twocan.Twocan
import app.twocan.go.mobile.Mobile
import app.twocan.userMessage
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/** Size of the QR PNG the core renders; it is drawn without smoothing. */
private const val QR_PIXELS = 768L

/** Our contact card as a QR code to scan, and as text to copy or share. */
@Composable
fun CardScreen(twocan: Twocan, onBack: () -> Unit) {
    val context = LocalContext.current
    var card by remember { mutableStateOf<String?>(null) }
    var qr by remember { mutableStateOf<ImageBitmap?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    LaunchedEffect(Unit) {
        runCatching {
            val text = twocan.call { it.myCard() }
            val png = withContext(Dispatchers.Default) { Mobile.cardQR(text, QR_PIXELS) }
            card = text
            qr = BitmapFactory.decodeByteArray(png, 0, png.size).asImageBitmap()
        }.onFailure { error = it.userMessage }
    }

    Scaffold(topBar = { BackBar("My card", onBack) }) { padding ->
        Column(
            Modifier.fillMaxSize().padding(padding).verticalScroll(rememberScrollState()).padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            Text("Let your contact scan this code with Twocan, or send them the text below.")
            error?.let { Text(it, color = MaterialTheme.colorScheme.error) }
            qr?.let {
                Image(
                    it,
                    contentDescription = "Your contact card as a QR code",
                    filterQuality = FilterQuality.None,
                    // White behind the code so it scans in dark mode too.
                    modifier = Modifier.fillMaxWidth().aspectRatio(1f).background(Color.White),
                )
            }
            card?.let { text ->
                SelectionContainer {
                    Text(text, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall)
                }
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedButton(onClick = { context.copyText("Twocan card", text) }) { Text("Copy") }
                    Button(onClick = { context.shareText("My Twocan card", text) }) { Text("Share") }
                }
            }
        }
    }
}
