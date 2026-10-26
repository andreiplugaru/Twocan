package app.twocan.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import app.twocan.Field
import app.twocan.Json
import app.twocan.Twocan
import app.twocan.go.mobile.Check
import app.twocan.go.mobile.CheckListener
import app.twocan.userMessage
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

/** How often the details refresh while shown. */
private const val REFRESH_MS = 2_000L

data class CheckStep(val title: String, val detail: String, val status: String)

data class CheckState(
    val running: Boolean = false,
    val steps: List<CheckStep> = emptyList(),
    val verdict: String = "",
    val report: String = "",
)

/** Connection details of one contact, and the connection check. */
class DetailsViewModel(private val twocan: Twocan, val peer: String) : ViewModel() {
    private val _fields = MutableStateFlow<List<Field>>(emptyList())
    val fields: StateFlow<List<Field>> = _fields.asStateFlow()
    private val _error = MutableStateFlow<String?>(null)
    val error: StateFlow<String?> = _error.asStateFlow()
    private val _check = MutableStateFlow(CheckState())
    val check: StateFlow<CheckState> = _check.asStateFlow()
    @Volatile private var running: Check? = null

    init {
        viewModelScope.launch {
            // A ping measures the latency the details show; it fails
            // harmlessly when they are offline.
            runCatching { twocan.call { it.ping(peer) } }
            while (isActive) {
                runCatching { twocan.call { Json.fields(it.details(peer)) } }
                    .onSuccess { _fields.value = it; _error.value = null }
                    .onFailure { _error.value = it.userMessage }
                delay(REFRESH_MS)
            }
        }
    }

    fun startCheck(name: String) {
        if (_check.value.running) return
        _check.value = CheckState(running = true)
        val listener = object : CheckListener {
            override fun onStep(index: Long, title: String, detail: String, status: String) {
                _check.update { s ->
                    val steps = s.steps.toMutableList()
                    val step = CheckStep(title, detail, status)
                    if (index < steps.size) steps[index.toInt()] = step else steps.add(step)
                    s.copy(steps = steps)
                }
            }

            override fun onDone(verdict: String, report: String) {
                _check.update { it.copy(running = false, verdict = verdict, report = report) }
                running = null
            }
        }
        viewModelScope.launch {
            runCatching { twocan.call { it.checkConnection(peer, name, listener) } }
                .onSuccess { running = it }
                .onFailure { e -> _check.update { it.copy(running = false, verdict = e.userMessage) } }
        }
    }

    override fun onCleared() {
        running?.cancel()
    }
}

@Composable
fun DetailsScreen(twocan: Twocan, peer: String, onBack: () -> Unit) {
    val vm: DetailsViewModel = viewModel(key = "details-$peer") { DetailsViewModel(twocan, peer) }
    val fields by vm.fields.collectAsStateWithLifecycle()
    val error by vm.error.collectAsStateWithLifecycle()
    val check by vm.check.collectAsStateWithLifecycle()
    val contacts by twocan.contacts.collectAsStateWithLifecycle()
    val name = contacts.firstOrNull { it.id == peer }?.name ?: ""
    val context = LocalContext.current

    Scaffold(topBar = { BackBar(name, onBack) }) { padding ->
        Column(
            Modifier.fillMaxSize().padding(padding).verticalScroll(rememberScrollState()).padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            error?.let { Text(it, color = MaterialTheme.colorScheme.error) }
            SelectionContainer {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    fields.forEach { f ->
                        Column {
                            Text(f.label, style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.primary)
                            Text(f.value, style = MaterialTheme.typography.bodyMedium)
                        }
                    }
                }
            }

            HorizontalDivider()
            Text("Check connection", style = MaterialTheme.typography.titleMedium)
            Text(
                "Tries every way of reaching them, step by step, and says what is in the way. It can take a minute.",
                style = MaterialTheme.typography.bodyMedium,
            )
            Button(onClick = { vm.startCheck(name) }, enabled = !check.running) {
                Text(if (check.running) "Checking…" else "Check connection")
            }
            check.steps.forEach { StepRow(it) }
            if (check.verdict.isNotEmpty()) {
                Text(check.verdict, fontWeight = FontWeight.Bold)
            }
            if (check.report.isNotEmpty()) {
                OutlinedButton(onClick = { context.copyText("Twocan connection check", check.report) }) {
                    Text("Copy results")
                }
            }
        }
    }
}

@Composable
private fun StepRow(s: CheckStep) {
    val (mark, color) = when (s.status) {
        "ok" -> "✓" to Color(0xFF2E7D32)
        "info" -> "•" to MaterialTheme.colorScheme.onSurfaceVariant
        "warn" -> "!" to Color(0xFFE65100)
        "fail" -> "✗" to MaterialTheme.colorScheme.error
        "skipped" -> "–" to MaterialTheme.colorScheme.onSurfaceVariant
        else -> "…" to MaterialTheme.colorScheme.onSurfaceVariant
    }
    Row {
        Text(mark, color = color, fontWeight = FontWeight.Bold, modifier = Modifier.width(24.dp))
        Column {
            Text(s.title, style = MaterialTheme.typography.bodyMedium, fontWeight = FontWeight.Medium)
            if (s.detail.isNotEmpty()) Text(s.detail, style = MaterialTheme.typography.bodySmall)
        }
    }
}
