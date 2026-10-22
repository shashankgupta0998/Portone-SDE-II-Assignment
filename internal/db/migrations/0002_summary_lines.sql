-- Summary sheet lines in the order of amazon_sample_output_report.xlsx.
-- summary_field is the value the mapping configs route amounts into.
-- Five lines have no config field routing into them; they get a reserved
-- field name and show 0 unless a mapping rule routes into that field.
INSERT INTO summary_lines (summary_field, section, label, consolidated_label, sort_order) VALUES
    ('sales_product_charges',                  'Sales',          'Product Charges',               'Product Charges',               1),
    ('sales_tax',                              'Sales',          'Tax',                           'Tax',                           2),
    ('sales_shipping',                         'Sales',          'Shipping',                      'Shipping',                      3),
    ('sales_amazon_fees',                      'Sales',          'Amazon fees',                   'Amazon Fees',                   4),
    ('sales_inventory_reimbursements',         'Sales',          'Inventory Reimbursements',      'Inventory Reimbursements',      5),
    ('sales_cross_account_debt_adjustment',    'Sales',          'Cross-account Debt Adjustment', 'Cross-Account Debt Adjustment', 6),
    ('sales_other',                            'Sales',          'Other',                         'Other',                         7),
    ('sales_fba_fees',                         'Sales',          'FBA Fees',                      'FBA Fees',                      8),
    ('sales_micro_deposit_failed',             'Sales',          'Micro Deposit (Failed)',        'Micro Deposit (Failed)',        9),
    ('refunded_expenses',                      'Refunds',        'Refund expenses',               'Refund Expenses',              10),
    ('refunded_sales',                         'Refunds',        'Refunded sales',                'Refunded Sales',               11),
    ('expenses_promotional_rebates',           'Expenses',       'Promo rebates',                 'Promo Rebates',                12),
    ('expenses_fba_fees',                      'Expenses',       'FBA fees',                      'FBA Fees',                     13),
    ('expenses_cost_of_advertising',           'Expenses',       'Cost of Advertising',           'Cost of Advertising',          14),
    ('expenses_shipping_charges',              'Expenses',       'Shipping Charges',              'Shipping Charges',             15),
    ('expenses_amazon_fees',                   'Expenses',       'Amazon fees',                   'Amazon Fees',                  16),
    ('expenses_reversed_reimbursements',       'Expenses',       'Reversed Reimbursements',       'Reversed Reimbursements',      17),
    ('expenses_cross_account_debt_adjustment', 'Expenses',       'Cross-account Debt Adjustment', 'Cross-Account Debt Adjustment',18),
    ('expenses_other',                         'Expenses',       'Other',                         'Other',                        19),
    ('bank_account_transfer_round_off',        'Expenses',       'Micro Deposit',                 'Micro Deposit',                20),
    ('paid_to_amazon',                         'Paid To Amazon', 'Paid To Amazon',                'Paid To Amazon',               21)
ON CONFLICT (summary_field) DO NOTHING;
