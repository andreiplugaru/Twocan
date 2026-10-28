package app.twocan

import org.json.JSONArray
import org.json.JSONObject

// The records the Go core (package twocan/mobile) sends as JSON, and their
// parsers. The field names match mobile.go's contactJSON, messageJSON,
// fieldJSON and Listener events.

data class Contact(
    val id: String,
    val name: String,
    val state: String,
    val connected: Boolean,
    val relayed: Boolean,
    val unread: Int,
) {
    val accepted get() = state == "accepted"
    val incoming get() = state == "pending_incoming"
    val outgoing get() = state == "pending_outgoing"

    /** How the link to them is, in a few words. */
    val linkText
        get() = when {
            incoming -> "wants to add you"
            outgoing -> "request sent"
            !connected -> "offline"
            relayed -> "via relay"
            else -> "direct"
        }
}

data class Message(
    val id: String,
    val peer: String,
    val out: Boolean,
    /** "" once deleted for everyone. */
    val text: String,
    /** Unix milliseconds: when sent (out) or received (in). */
    val at: Long,
    /** pending, delivered or read (out), received (in). */
    val status: String,
    /** The ID of the message this one answers; "" if none. */
    val replyTo: String = "",
    /** Unix milliseconds; 0 if never edited. */
    val editedAt: Long = 0,
    /** Deleted for everyone: only "This message was deleted" is left. */
    val deleted: Boolean = false,
    val myReaction: String = "",
    val theirReaction: String = "",
) {
    val delivered get() = status == "delivered"
    val read get() = status == "read"
    val edited get() = editedAt != 0L
}

/** A run of formatted text, from the Go core's Format. */
data class Span(val text: String, val bold: Boolean, val italic: Boolean, val url: String)

data class Field(val label: String, val value: String)

/** An event from the Go core's Listener. */
sealed interface NodeEvent {
    data class MessageArrived(val message: Message) : NodeEvent
    data class Delivered(val peer: String, val id: String) : NodeEvent
    /** Messages were edited, deleted, reacted to or read. */
    data class Updated(val peer: String, val ids: List<String>) : NodeEvent
    data class Typing(val peer: String) : NodeEvent
    data object ContactsChanged : NodeEvent
    data class Reachability(val text: String) : NodeEvent
}

object Json {
    fun contacts(s: String): List<Contact> = objects(s).map {
        Contact(
            id = it.getString("id"),
            name = it.getString("name"),
            state = it.getString("state"),
            connected = it.getBoolean("connected"),
            relayed = it.getBoolean("relayed"),
            unread = it.getInt("unread"),
        )
    }

    fun messages(s: String): List<Message> = objects(s).map(::message)

    fun message(s: String): Message = message(JSONObject(s))

    fun fields(s: String): List<Field> = objects(s).map {
        Field(it.getString("label"), it.getString("value"))
    }

    /** Parses a Listener event; null for a type this app does not know. */
    fun event(s: String): NodeEvent? {
        val o = JSONObject(s)
        return when (o.getString("type")) {
            "message" -> NodeEvent.MessageArrived(message(o.getJSONObject("message")))
            "delivered" -> NodeEvent.Delivered(o.getString("peer"), o.getString("id"))
            "updated" -> {
                val ids = o.getJSONArray("ids")
                NodeEvent.Updated(o.getString("peer"), List(ids.length()) { ids.getString(it) })
            }
            "typing" -> NodeEvent.Typing(o.getString("peer"))
            "contacts" -> NodeEvent.ContactsChanged
            "reachability" -> NodeEvent.Reachability(o.getString("text"))
            else -> null
        }
    }

    fun spans(s: String): List<Span> = objects(s).map {
        Span(it.getString("text"), it.getBoolean("bold"), it.getBoolean("italic"), it.getString("url"))
    }

    // optX: fields an older Go core does not send.
    private fun message(o: JSONObject) = Message(
        id = o.getString("id"),
        peer = o.getString("peer"),
        out = o.getBoolean("out"),
        text = o.getString("text"),
        at = o.getLong("at"),
        status = o.getString("status"),
        replyTo = o.optString("replyTo"),
        editedAt = o.optLong("editedAt"),
        deleted = o.optBoolean("deleted"),
        myReaction = o.optString("myReaction"),
        theirReaction = o.optString("theirReaction"),
    )

    private fun objects(s: String): List<JSONObject> {
        val a = JSONArray(s)
        return List(a.length()) { a.getJSONObject(it) }
    }
}
