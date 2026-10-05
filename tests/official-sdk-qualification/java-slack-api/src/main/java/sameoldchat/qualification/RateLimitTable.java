package sameoldchat.qualification;

import com.slack.api.methods.MethodsRateLimits;

import java.util.Map;
import java.util.TreeMap;

/**
 * Prints the pinned slack-api-client's published Web API rate-limit tiers
 * (com.slack.api.methods.MethodsRateLimits) as the JSON document vendored at
 * specs/upstream/java-slack-sdk/methods-rate-limits.json. qualify.sh compares
 * the two, so the vendored table the server's tiers are tested against is
 * exactly the table the pinned SDK ships.
 */
public final class RateLimitTable {
    private RateLimitTable() {}

    public static void main(String[] args) {
        TreeMap<String, String> tiers = new MethodsRateLimits().toMap();
        StringBuilder out = new StringBuilder("{\n");
        int remaining = tiers.size();
        for (Map.Entry<String, String> entry : tiers.entrySet()) {
            out.append("  \"").append(entry.getKey()).append("\": \"").append(entry.getValue()).append('"');
            out.append(--remaining > 0 ? ",\n" : "\n");
        }
        out.append("}\n");
        System.out.print(out);
    }
}
